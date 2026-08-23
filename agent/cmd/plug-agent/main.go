// Command plug-agent is the standalone agent: the container's ENTRYPOINT, and
// the binary its own SSH server re-executes to run a verb.
//
// It is deliberately this thin. Everything lives in the importable package, so
// that Meerkat linking the agent into its own binary and this container running
// it alone execute the SAME code, rather than two copies that drift.
package main

import "github.com/softwarity/plug/agent"

func main() { agent.Main() }
