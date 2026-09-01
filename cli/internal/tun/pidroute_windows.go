//go:build windows

package tun

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

// The Windows connect-time attribution primitives, mirrors of the Linux/macOS
// ones: ppidOf walks the process table (ToolHelp snapshot), pidForLocalPort maps
// a local TCP source port to its owning PID (GetExtendedTcpTable). These are the
// bricks the multicluster router needs; wiring them into an N-tunnel datapath on
// Windows (a persistent SYSTEM service, like the macOS daemon) is the remaining
// step — the single-cluster Windows path never calls these.

// procStart returns pid's creation time as raw FILETIME ticks (100 ns since 1601),
// via OpenProcess + GetProcessTimes. Numeric and boot-stable; only comparability
// feeds the ancestry walk, so a recycled PID gets a strictly newer stamp and a
// parent younger than its child is rejected. QUERY_LIMITED_INFORMATION is the
// least-privileged handle that still yields times, and a SYSTEM service can open
// any process. We assemble the 64-bit tick count ourselves rather than lean on a
// FILETIME→epoch helper — the offset is a constant and only ordering matters.
func procStart(pid int) (int64, bool) {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return 0, false
	}
	defer windows.CloseHandle(h)
	var creation, exit, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(h, &creation, &exit, &kernel, &user); err != nil {
		return 0, false
	}
	return int64(uint64(creation.HighDateTime)<<32 | uint64(creation.LowDateTime)), true
}

// ppidOf returns pid's parent PID via a ToolHelp process snapshot.
func ppidOf(pid int) (int, bool) {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return 0, false
	}
	defer windows.CloseHandle(snap)
	var pe windows.ProcessEntry32
	pe.Size = uint32(unsafe.Sizeof(pe))
	if err := windows.Process32First(snap, &pe); err != nil {
		return 0, false
	}
	for {
		if int(pe.ProcessID) == pid {
			return int(pe.ParentProcessID), true
		}
		if err := windows.Process32Next(snap, &pe); err != nil {
			return 0, false // walked the whole table without a match
		}
	}
}

var (
	modiphlpapi             = windows.NewLazySystemDLL("iphlpapi.dll")
	procGetExtendedTcpTable = modiphlpapi.NewProc("GetExtendedTcpTable")
)

// mibTCPRowOwnerPID mirrors MIB_TCPROW_OWNER_PID. dwLocalPort holds the port in
// network byte order in its low 16 bits.
type mibTCPRowOwnerPID struct {
	state      uint32
	localAddr  uint32
	localPort  uint32
	remoteAddr uint32
	remotePort uint32
	owningPid  uint32
}

const tcpTableOwnerPidAll = 5 // TCP_TABLE_OWNER_PID_ALL

// pidForLocalPort maps a local TCP source port to the PID owning that socket, via
// GetExtendedTcpTable (MIB_TCPTABLE_OWNER_PID). A SYSTEM service sees every
// process's connections. Two calls: size, then the table.
func pidForLocalPort(srcPort uint16) (int, bool) {
	var size uint32
	procGetExtendedTcpTable.Call(0, uintptr(unsafe.Pointer(&size)), 0,
		uintptr(windows.AF_INET), tcpTableOwnerPidAll, 0)
	if size == 0 {
		return 0, false
	}
	buf := make([]byte, size)
	r, _, _ := procGetExtendedTcpTable.Call(uintptr(unsafe.Pointer(&buf[0])),
		uintptr(unsafe.Pointer(&size)), 0, uintptr(windows.AF_INET), tcpTableOwnerPidAll, 0)
	if r != 0 { // NO_ERROR == 0
		return 0, false
	}
	n := *(*uint32)(unsafe.Pointer(&buf[0])) // DWORD dwNumEntries, then the rows
	if n == 0 {
		return 0, false
	}
	rows := unsafe.Slice((*mibTCPRowOwnerPID)(unsafe.Pointer(&buf[4])), n)
	for i := range rows {
		lp := rows[i].localPort
		port := uint16((lp&0xff)<<8 | (lp>>8)&0xff) // ntohs of the low 16 bits
		if port == srcPort {
			return int(rows[i].owningPid), true
		}
	}
	return 0, false
}

// uidOf has no Windows answer yet: accounts are SIDs, not integers, and the
// token of another session's process is not readable the way `ps -o uid=` is.
// Reporting "unknown" keeps the single-cluster shortcut behaving exactly as it
// did, which is the correct default here: this file's job is to not regress
// Windows while the macOS leak is closed. The Windows equivalent is its own
// piece of work, tracked with the ProgramData finding.
func uidOf(int) (int, bool) { return 0, false }
