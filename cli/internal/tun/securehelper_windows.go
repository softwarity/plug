package tun

// Windows resolves its own helpers, and the reason the Unix side cannot is
// absent here: plug is not setuid on Windows, it elevates per launch, so there
// is no gap between the privilege plug holds and the one its caller has. netsh
// lives under %SystemRoot%\System32, which is not a fixed string across
// installs, and hardcoding one would break the machines that moved it.
func helperPath(bin string) (string, bool) { return bin, true }

func helperDirsList() string { return "the system's own search path" }

// HelperPath: see the Unix file. Windows resolves its own.
func HelperPath(bin string) string { return bin }
