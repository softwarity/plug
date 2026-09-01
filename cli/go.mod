module github.com/softwarity/plug/cli

go 1.26.3

require (
	golang.org/x/crypto v0.55.0
	golang.org/x/sys v0.47.0
	golang.org/x/term v0.45.0
	golang.zx2c4.com/wireguard v0.0.0-20260522210424-ecfc5a8d5446
	golang.zx2c4.com/wireguard/windows v1.0.1
	// Upgrade with `go get gvisor.dev/gvisor@go`, never @latest. gvisor publishes
	// no tags, and @latest resolves to master, whose pkg/tcpip/stack currently
	// carries a bridge_test.go declaring a different package: the go tool refuses
	// the directory outright ("found packages stack and bridge"). The `go` branch
	// is the one meant for module consumers and builds cleanly.
	gvisor.dev/gvisor v0.0.0-20260901202214-9028bcbc4fc4
)

require (
	github.com/google/btree v1.1.2 // indirect
	golang.org/x/exp v0.0.0-20250711185948-6ae5c78190dc // indirect
	golang.org/x/net v0.57.0 // indirect
	golang.org/x/time v0.15.0 // indirect
	golang.zx2c4.com/wintun v0.0.0-20230126152724-0fa3db229ce2 // indirect
)
