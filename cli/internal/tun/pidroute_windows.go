//go:build windows

package tun

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

// The Windows connect-time attribution primitives, mirrors of the Linux/macOS
// ones: ppidOf walks the process table (ToolHelp snapshot), pidForFlow maps a
// flow to the PID owning its socket (GetExtendedTcpTable). The
// multicluster router (router.go) feeds them to the SYSTEM service's datapath,
// the Windows counterpart of the macOS daemon, and the client registry reads
// procStart to stamp and verify its markers.

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

// mibTCPRowOwnerPID mirrors MIB_TCPROW_OWNER_PID. The ports hold the port in
// network byte order in their low 16 bits, the addresses the address in network
// byte order.
type mibTCPRowOwnerPID struct {
	state      uint32
	localAddr  uint32
	localPort  uint32
	remoteAddr uint32
	remotePort uint32
	owningPid  uint32
}

const tcpTableOwnerPidAll = 5 // TCP_TABLE_OWNER_PID_ALL

// pidForFlow maps a flow to the PID owning its socket, via GetExtendedTcpTable
// (MIB_TCPTABLE_OWNER_PID). A SYSTEM service sees every process's connections.
// Two calls: size, then the table.
func pidForFlow(f flow) (int, bool) {
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
	return pidFromTCPRows(unsafe.Slice((*mibTCPRowOwnerPID)(unsafe.Pointer(&buf[4])), n), f)
}

// pidFromTCPRows is the matching half, separated so it can be tested against
// rows written out instead of whatever this machine happens to be doing.
//
// The row is the flow's when its local port is the flow's source port and its
// remote end is exactly the flow's destination, the fake address and port.
// The port alone named a listener on it just as well (the table holds them,
// TCP_TABLE_OWNER_PID_ALL), or another socket of the same port to somewhere
// else, and the first such row answered for the flow: the wrong cluster in the
// multicluster router, the wrong owner in the single-cluster check. The fake
// destination is minted by plug, so no other socket has it.
func pidFromTCPRows(rows []mibTCPRowOwnerPID, f flow) (int, bool) {
	a := f.dst.Addr().As4()
	// The address as the row holds it: its four bytes in network order, read
	// as a little-endian DWORD, which is how Windows lays them in memory.
	wantAddr := uint32(a[0]) | uint32(a[1])<<8 | uint32(a[2])<<16 | uint32(a[3])<<24
	for i := range rows {
		if ntohs(rows[i].localPort) == f.srcPort &&
			rows[i].remoteAddr == wantAddr && ntohs(rows[i].remotePort) == f.dst.Port() {
			return int(rows[i].owningPid), true
		}
	}
	return 0, false
}

// ntohs reads a port held in network byte order in a DWORD's low 16 bits.
func ntohs(p uint32) uint16 { return uint16((p&0xff)<<8 | (p>>8)&0xff) }

// The account behind a pid, the question the single-cluster check asks on
// every SYN, is accountOfPID in account_windows.go: a SID read off the
// process's token, beside thisAccount which reads this process's own.
