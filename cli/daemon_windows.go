//go:build windows

package main

import (
	"os"
	"os/exec"
	"path/filepath"

	"github.com/softwarity/plug/cli/internal/tun"
	"github.com/softwarity/plug/cli/internal/tunnel"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

// The Windows GLOBAL datapath lives in an SCM service (the counterpart of the macOS
// detached daemon). It runs as SYSTEM, holds ONE WinTUN + netstack + NRPT DNS and a
// tunnel per active cluster (routed by PID at connect), and is reconciled against the
// client registry (registry_windows.go). Because the privileged work is in the
// service, a non-elevated `plug <cmd>` only drops a client marker — that is what
// removes the per-run Administrator requirement, and holding N tunnels is what gives
// multicluster. Model mirrors WireGuard-Windows (service + WinTUN + thin launcher).

// daemonMain is the entry point when the SCM starts the service (plug.exe DaemonVerb).
func daemonMain(_ []string) int {
	// A service has no console — route info()'s output (os.Stderr/Stdout) to a file.
	if f, err := os.OpenFile(serviceLogPath(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644); err == nil {
		os.Stderr = f
		os.Stdout = f
	}
	if err := svc.Run(tun.ServiceName, &plugService{}); err != nil {
		info("service: run: %v", err)
		return 1
	}
	return 0
}

func serviceLogPath() string {
	pd := os.Getenv("ProgramData")
	if pd == "" {
		pd = `C:\ProgramData`
	}
	dir := filepath.Join(pd, "plug")
	_ = os.MkdirAll(dir, 0o755)
	return filepath.Join(dir, "service.log")
}

type plugService struct{}

// Execute is the SCM service body: bring up the global datapath, reconcile tunnels
// against the client registry, and run until the SCM stops us or the reaper tears the
// datapath down (no cluster with a live client for the grace period).
func (s *plugService) Execute(_ []string, r <-chan svc.ChangeRequest, changes chan<- svc.Status) (bool, uint32) {
	const accepts = svc.AcceptStop | svc.AcceptShutdown
	changes <- svc.Status{State: svc.StartPending}

	ct := tun.NewClusterTransports()
	dp, err := tun.StartGlobalDatapath(ct, info)
	if err != nil {
		info("service: datapath: %v", err)
		changes <- svc.Status{State: svc.Stopped}
		return true, 1
	}
	tunnels := map[string]*tunnel.Transport{}
	reconcileOnce(ct, tunnels) // open the starting cluster's tunnel before RUNNING
	changes <- svc.Status{State: svc.Running, Accepts: accepts}
	info("service: global datapath up (multicluster), %d cluster(s)", len(tunnels))

	stop := make(chan struct{})
	reconciled := reconcileLoop(ct, tunnels, stop)
	go reapGlobal(dp, stop)

	dpDone := make(chan struct{})
	go func() { dp.Wait(); close(dpDone) }()

loop:
	for {
		select {
		case c := <-r:
			switch c.Cmd {
			case svc.Interrogate:
				changes <- c.CurrentStatus
			case svc.Stop, svc.Shutdown:
				break loop
			default:
			}
		case <-dpDone: // the reaper stopped the datapath — no clients left
			break loop
		}
	}

	changes <- svc.Status{State: svc.StopPending}
	close(stop)
	dp.Stop()
	<-reconciled // a tick in flight still writes to `tunnels` — let it finish
	closeAll(ct, tunnels)
	changes <- svc.Status{State: svc.Stopped}
	info("service: global datapath down")
	return false, 0
}

// startService asks the SCM to start the plug service. A non-elevated launcher can do
// this because installService granted Authenticated Users SERVICE_START on the service
// (the SDDL in installService) — the crux of "run without admin".
func startService() error {
	scm, err := windows.OpenSCManager(nil, nil, windows.SC_MANAGER_CONNECT)
	if err != nil {
		return err
	}
	defer windows.CloseServiceHandle(scm)
	name, err := windows.UTF16PtrFromString(tun.ServiceName)
	if err != nil {
		return err
	}
	sh, err := windows.OpenService(scm, name, windows.SERVICE_START|windows.SERVICE_QUERY_STATUS)
	if err != nil {
		return err // not installed, or ACL denies start
	}
	defer windows.CloseServiceHandle(sh)
	if err := windows.StartService(sh, 0, nil); err != nil {
		if err == windows.ERROR_SERVICE_ALREADY_RUNNING {
			return nil
		}
		return err
	}
	return nil
}

// cmdDown stops the running service (`plug down`).
func cmdDown(_ []string) {
	scm, err := windows.OpenSCManager(nil, nil, windows.SC_MANAGER_CONNECT)
	if err != nil {
		info("no SCM access: %v", err)
		return
	}
	defer windows.CloseServiceHandle(scm)
	name, _ := windows.UTF16PtrFromString(tun.ServiceName)
	sh, err := windows.OpenService(scm, name, windows.SERVICE_STOP|windows.SERVICE_QUERY_STATUS)
	if err != nil {
		info("no plug service running")
		return
	}
	defer windows.CloseServiceHandle(sh)
	var st windows.SERVICE_STATUS
	if err := windows.ControlService(sh, windows.SERVICE_CONTROL_STOP, &st); err != nil {
		info("stopping service: %v", err)
		return
	}
	info("stopped the plug service")
}

// installService creates the SCM service (the ONE admin step — the installer runs
// `plug install-service` elevated). The service is on-demand (StartManual): the
// launcher starts it via the SCM when needed and the reaper stops it when idle. The
// SDDL then grants Authenticated Users START/STOP so day-to-day `plug <cmd>` runs
// unelevated — the whole point. Points the service at THIS exe (which sits beside
// wintun.dll after install) with the daemon verb. Idempotent.
func installService() {
	exe, err := os.Executable()
	if err != nil {
		fatal("locate plug.exe: %v", err)
	}
	m, err := mgr.Connect()
	if err != nil {
		fatal("connect to the service manager (run this elevated): %v", err)
	}
	defer m.Disconnect()
	if s, err := m.OpenService(tun.ServiceName); err == nil {
		s.Close()
		info("service %q already installed", tun.ServiceName)
		return
	}
	s, err := m.CreateService(tun.ServiceName, exe, mgr.Config{
		DisplayName: "plug cluster datapath",
		Description: "Holds the plug userspace-TUN datapath so plug reaches cluster services without per-run admin.",
		StartType:   mgr.StartManual,
	}, tun.DaemonVerb)
	if err != nil {
		fatal("create service (run this elevated): %v", err)
	}
	defer s.Close()
	// Grant Authenticated Users START/STOP/QUERY so a non-elevated launcher can bring
	// the service up. sc.exe via exec.Command keeps the SDDL a single clean arg (no
	// shell quoting). SY=SYSTEM, BA=Builtin Admins, AU=Authenticated Users.
	const sddl = "D:(A;;CCLCSWRPWPDTLOCRRC;;;SY)(A;;CCDCLCSWRPWPDTLOCRSDRCWDWO;;;BA)(A;;CCLCSWRPWPLORC;;;AU)"
	if out, err := exec.Command("sc.exe", "sdset", tun.ServiceName, sddl).CombinedOutput(); err != nil {
		info("warning: could not set the service ACL (%v: %s) — non-admin start may fail", err, out)
	}
	info("service %q installed (on-demand). Day-to-day `plug <cmd>` now needs no admin.", tun.ServiceName)
}

// removeService deletes the SCM service (`plug remove-service`, elevated).
func removeService() {
	m, err := mgr.Connect()
	if err != nil {
		fatal("connect to the service manager (run this elevated): %v", err)
	}
	defer m.Disconnect()
	s, err := m.OpenService(tun.ServiceName)
	if err != nil {
		info("service %q not installed", tun.ServiceName)
		return
	}
	defer s.Close()
	if err := s.Delete(); err != nil {
		fatal("delete service: %v", err)
	}
	info("service %q removed", tun.ServiceName)
}
