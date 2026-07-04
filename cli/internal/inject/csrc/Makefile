# Build the plug hook shared library (the "N1" transparent connect()/DNS layer).
#
# The dev machine is macOS arm64 — `make` (default) builds that. Other targets
# cross-compile where a toolchain allows it. The macOS .dylib CANNOT be produced
# by the Linux/alpine agent image (no Apple SDK / no clang-with-darwin target),
# so its distribution is handled separately; see the package README.
#
# Outputs go to ./build/<os>-<arch>/plug-hook.{dylib,so}.

CC      ?= clang
SRC     := plug_hook.c
CFLAGS  := -O2 -Wall -Wextra -fPIC -fvisibility=hidden
BUILD   := build

.PHONY: all darwin-arm64 darwin-amd64 darwin-universal linux-amd64 linux-arm64 clean

# Default: the dev machine (macOS arm64).
all: darwin-arm64

darwin-arm64:
	@mkdir -p $(BUILD)/darwin-arm64
	$(CC) $(CFLAGS) -arch arm64 -dynamiclib -o $(BUILD)/darwin-arm64/plug-hook.dylib $(SRC)
	@echo "built $(BUILD)/darwin-arm64/plug-hook.dylib"

darwin-amd64:
	@mkdir -p $(BUILD)/darwin-amd64
	$(CC) $(CFLAGS) -arch x86_64 -dynamiclib -o $(BUILD)/darwin-amd64/plug-hook.dylib $(SRC)
	@echo "built $(BUILD)/darwin-amd64/plug-hook.dylib"

# A single fat dylib covering both macOS architectures (what we ship for macOS).
darwin-universal:
	@mkdir -p $(BUILD)/darwin-universal
	$(CC) $(CFLAGS) -arch arm64 -arch x86_64 -dynamiclib \
		-o $(BUILD)/darwin-universal/plug-hook.dylib $(SRC)
	@echo "built $(BUILD)/darwin-universal/plug-hook.dylib"

# Linux targets. On a Linux host with gcc/clang this "just works". Cross from
# macOS needs a linux-targeting toolchain (e.g. `zig cc -target ...`); set CC.
linux-amd64:
	@mkdir -p $(BUILD)/linux-amd64
	$(CC) $(CFLAGS) -shared -o $(BUILD)/linux-amd64/plug-hook.so $(SRC) -ldl
	@echo "built $(BUILD)/linux-amd64/plug-hook.so"

linux-arm64:
	@mkdir -p $(BUILD)/linux-arm64
	$(CC) $(CFLAGS) -shared -o $(BUILD)/linux-arm64/plug-hook.so $(SRC) -ldl
	@echo "built $(BUILD)/linux-arm64/plug-hook.so"

clean:
	rm -rf $(BUILD)
