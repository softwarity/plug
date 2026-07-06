//go:build windows

package tun

// On Windows the attribution primitives will use the ToolHelp snapshot
// (CreateToolhelp32Snapshot → PROCESSENTRY32.th32ParentProcessID) for ppidOf and
// GetExtendedTcpTable (MIB_TCPTABLE_OWNER_PID) for pidForLocalPort. Stubbed until
// the multicluster daemon is wired (docs/multicluster.md); the single-cluster
// path never calls these.

func ppidOf(int) (int, bool)             { return 0, false }
func pidForLocalPort(uint16) (int, bool) { return 0, false }
