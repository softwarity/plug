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
