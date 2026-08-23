// Importable path, because Meerkat compiles this INTO its own binary: the
// agent's server, key handling and notifications become function calls there,
// while the verbs keep running as a subprocess. A local module name (plug-agent)
// cannot be imported at all.
module github.com/softwarity/plug/agent/helper

go 1.26

require golang.org/x/crypto v0.53.0

require golang.org/x/sys v0.46.0 // indirect
