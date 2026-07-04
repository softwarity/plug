# Embedded hook libraries

plug embeds the compiled hook library here (via go:embed) and writes it out to
~/.plug/lib/ at runtime, then injects it into the child (DYLD_INSERT_LIBRARIES /
LD_PRELOAD). Files are named plug-hook-<os>-<arch>.{dylib,so}.

These binaries are produced by `cli/internal/inject/Makefile` (clang) and copied
here by the build. They are NOT committed for every platform: on the dev machine
(macOS arm64) the local build fills in the darwin-arm64 dylib. Cross-building the
macOS .dylib inside the Linux agent image is not yet solved (no Apple SDK there),
so a plug binary built in that pipeline may ship without a macOS hook — injection
then silently disables and the env-proxy + forwards still work.
