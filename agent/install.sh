#!/bin/bash
# Windows installer, run from Git Bash (the assumed Windows dev shell):
#
#   ssh -p 2222 -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
#       get@<host> install-windows | bash -s -- <host> <port>
#
# Bash — not PowerShell — on purpose: Git Bash is mandatory on Windows anyway, a
# piped bash script's `exit` is reliable (a piped `powershell -Command -` script's
# is not), and the whole flow is one shell. NO admin is needed to INSTALL; the
# datapath SERVICE needs admin ONCE (run Git Bash as Administrator), after which
# every `plug <cmd>` runs with no admin. The MSYS ssh that streams this script
# can't have its Win32 command line read, so the cluster host is passed as an
# argument (`bash -s -- <host> [port]`), not inferred.
set -euo pipefail

info() { echo "plug: $1"; }
die()  { echo "plug: $1" >&2; exit 1; }

HOST="${1:-}"
PORT="${2:-2222}"
[ -n "$HOST" ] || die "pass the cluster host on the bash side of the pipe:
  ... get@<host> install-windows | bash -s -- <host> [port]"
command -v ssh >/dev/null 2>&1 || die "ssh not found — install the Windows OpenSSH client (or use Git's ssh)"

# Install dir: %LOCALAPPDATA%\Programs\plug (also the service's binPath). Keep a
# Windows form (for PATH / the service) and a unix form (for redirects here).
WIN_DIR="${LOCALAPPDATA}\\Programs\\plug"
DIR="$(cygpath "$LOCALAPPDATA" 2>/dev/null || printf '%s' "$LOCALAPPDATA")/Programs/plug"
mkdir -p "$DIR"

# -n (stdin from /dev/null): ssh otherwise forwards stdin to the channel, and when
# this script runs piped (`... install-windows | bash`) that stdin is a pipe with no
# EOF — so ssh would stream the whole binary yet never exit, hanging the install.
SSH_OPTS=(-n -p "$PORT" -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR -o BatchMode=yes)

# 1. plug.exe — streamed raw over ssh to a FILE (a bash redirect: byte-exact, and
#    unlike Go's captured pipe it doesn't hang the Windows ssh).
info "downloading plug.exe from $HOST ..."
ssh "${SSH_OPTS[@]}" "get@$HOST" windows-amd64 > "$DIR/plug.exe"
sz=$(wc -c < "$DIR/plug.exe" | tr -d ' ')
[ "${sz:-0}" -gt 1000000 ] || die "plug.exe download failed ($sz bytes)"
[ "$(head -c2 "$DIR/plug.exe")" = "MZ" ] || die "plug.exe is not a Windows executable (bad PE header)"
info "installed to $DIR/plug.exe"

# 2. wintun.dll — from the AGENT (baked into the image), not wintun.net: no runtime
#    dependency on an external host, and no more intermittent fetch. WinTUN loads it
#    from the exe's own dir, so it lands beside plug.exe.
info "downloading wintun.dll ..."
ssh "${SSH_OPTS[@]}" "get@$HOST" wintun > "$DIR/wintun.dll"
[ "$(wc -c < "$DIR/wintun.dll" | tr -d ' ')" -gt 100000 ] || die "wintun.dll download failed"
info "installed wintun.dll"

# 3. Profile, named after the host (a second cluster adds a second profile).
mkdir -p "$HOME/.plug"
conf="$HOME/.plug/$HOST.conf"
if [ -e "$conf" ]; then
  info "profile '$HOST' already configured"
else
  printf 'host = %s\nport = %s\n' "$HOST" "$PORT" > "$conf"
  info "profile '$HOST' -> $HOST:$PORT"
fi

# 4. PATH (user scope) — append via the registry + setx, only if missing. setx
#    writes HKCU\Environment; reading the current user Path first avoids clobbering.
# `|| true` is not decoration. This script runs under `set -euo pipefail`, and a
# fresh Windows user has NO Path value under HKCU: `reg query` then exits
# non-zero, pipefail promotes that over sed's success, and the installer aborts
# HALFWAY THROUGH - binary already in place, PATH never set, and no message
# saying why. The empty answer is the correct one for that machine; it is not an
# error, and the case below already handles it.
cur=$(reg query 'HKCU\Environment' //v Path 2>/dev/null | sed -n 's/.*REG_\(EXPAND_\)\?SZ[[:space:]]*//p' | tr -d '\r' || true)
case ";${cur};" in
  *";${WIN_DIR};"*) info "already on PATH" ;;
  *)
    # setx TRUNCATES silently past 1024 characters. It warns on stderr and then
    # exits 0, so `>/dev/null 2>&1 && info "added to PATH"` reported success while
    # amputating the user's PATH: everything they had installed before plug, gone,
    # and the installer saying it went well. The length is checked before writing,
    # and the output is kept so a failure can say what it was.
    _new="${cur:+$cur;}$WIN_DIR"
    if [ "${#_new}" -ge 1024 ]; then
      info "your user PATH is ${#_new} characters, and setx silently truncates at 1024."
      info "Refusing to write it. Add $WIN_DIR to your PATH by hand, or shorten it first."
    elif _out=$(setx Path "$_new" 2>&1); then
      info "added to PATH (open a new terminal for it to take effect)"
    else
      info "could not add $WIN_DIR to your PATH: $_out"
    fi
    ;;
esac

# 5. The datapath SERVICE — needs admin once. If Git Bash is elevated it installs
#    now; otherwise plug.exe reports it and we tell the user to re-run elevated.
info "installing the datapath service (needs admin once)..."
if "$DIR/plug.exe" install-service >/dev/null 2>&1; then
  info "service installed — 'plug <cmd>' now runs without admin, multicluster included"
else
  info "not elevated: to enable no-admin runs, once — open Git Bash as Administrator and run: plug install-service"
fi
info "ready. Open a new Git Bash and:  plug -p $HOST <your command>"
