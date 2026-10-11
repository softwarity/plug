#!/bin/sh
set -e

# Pre-create a profile from *your* ssh command, named after the cluster host,
# or as the command said (`install <name>`, set above as _name).
# The agent can't know which address you reached it on (a routing mesh hides
# it), but your `ssh ... get@<host>` line does — and while this installer
# streams, that ssh is still alive in the process table (blocked sending the
# embedded binaries). So read host/port off it and save ~/.plug/<host>.conf,
# so the first `plug` run needs no wizard. Naming by host means installing from
# a second cluster (get@node1) adds a second profile rather than being ignored,
# so `plug -p node0` and `plug -p node1` can run side by side. Best-effort:
# silent if it can't tell (first run then falls back to the wizard); never
# clobbers an existing profile for that host.
_cmd=$(ps -A -o args= 2>/dev/null | grep 'get@' | grep -w install | grep -v grep | head -n1)
_host=$(printf '%s\n' "$_cmd" | sed -n 's/.*get@\([A-Za-z0-9._-]*\).*/\1/p')
_port=$(printf '%s\n' "$_cmd" | sed -n 's/.*-p[ =]*\([0-9][0-9]*\).*/\1/p')
_pname=${_name:-$_host}
if [ -n "$_host" ]; then
  _conf="$HOME/.plug/$_pname.conf"
  if [ -e "$_conf" ]; then
    _profile="'$_pname' (already configured)"
  else
    mkdir -p "$HOME/.plug"
    printf 'host = %s\nport = %s\n' "$_host" "${_port:-2222}" > "$_conf"
    _profile="'$_pname' -> $_host:${_port:-2222}"
  fi
fi

os=$(uname -s | tr 'A-Z' 'a-z')
arch=$(uname -m)
case "$arch" in x86_64|amd64) arch=amd64 ;; aarch64|arm64) arch=arm64 ;; esac
for d in "$HOME/.local/bin" /usr/local/bin; do
  mkdir -p "$d" 2>/dev/null && [ -w "$d" ] && dest="$d" && break
done
: "${dest:?plug: no writable bin dir found (need ~/.local/bin or /usr/local/bin)}"
decode() { base64 -d 2>/dev/null || base64 -D; }
# @@ the emit_<os>_<arch> functions, one per binary, go here @@
# A previous install may have left plug as a setuid-root helper (macOS) or a
# root-owned cap binary (Linux): we own the DIRECTORY, so unlink it first (a `>`
# redirect can't overwrite a root file in place — "Permission denied" — and can't
# replace a running one either). rm on the dir entry always works; the new file
# is written fresh under our uid.
rm -f "$dest/plug" 2>/dev/null || true
case "$os-$arch" in
  linux-amd64)  emit_linux_amd64  > "$dest/plug" ;;
  linux-arm64)  emit_linux_arm64  > "$dest/plug" ;;
  darwin-amd64) emit_darwin_amd64 > "$dest/plug" ;;
  darwin-arm64) emit_darwin_arm64 > "$dest/plug" ;;
  *) echo "plug: unsupported platform $os-$arch (on Windows, use Git Bash: ssh get@<host> install-windows | bash -s -- <host> <port>)" >&2; exit 1 ;;
esac
chmod +x "$dest/plug"
echo "plug: installed to $dest/plug"
# Grant the privilege plug needs (TUN device + routes, per-launch mount ns, bind
# :53) ONCE, so day-to-day `plug <command>` runs with no sudo. sudo prompts on
# /dev/tty even through `| sh`. Linux = file capabilities; macOS has none, so it's
# a setuid-root helper that drops your command back to you (see privdrop_unix.go).
if [ "$os" = linux ]; then
  if command -v setcap >/dev/null 2>&1; then
    echo "plug: granting network capabilities (one sudo now → future runs need none)…"
    if sudo setcap cap_net_admin,cap_sys_admin,cap_net_bind_service+ep "$dest/plug"; then
      echo "plug: ready — 'plug <command>' runs without sudo"
    else
      echo "plug: setcap failed — meanwhile run 'sudo plug <command>', or fix with:" >&2
      echo "      sudo setcap cap_net_admin,cap_sys_admin,cap_net_bind_service+ep $dest/plug" >&2
    fi
  else
    echo "plug: 'setcap' missing (install libcap2-bin) — meanwhile run 'sudo plug <command>'" >&2
  fi
elif [ "$os" = darwin ]; then
  echo "plug: installing the privileged helper (one sudo now → future runs need none)…"
  # The core store, created root-owned while we already hold this one sudo.
  # macOS runs the cached core AS ROOT (the setuid helper drops privilege for
  # YOUR command, not for the core), so a store the user can write is a store
  # the user can hand root a different binary from. plug creates and cleans it
  # itself afterwards — nobody types sudo again, including to uninstall.
  sudo install -d -o root -g wheel -m 755 /var/db/plug /var/db/plug/versions 2>/dev/null \
    || echo "plug: could not create /var/db/plug — plug will say so on first run" >&2
  if sudo chown root:wheel "$dest/plug" && sudo chmod u+s "$dest/plug"; then
    echo "plug: ready — 'plug <command>' runs without sudo"
  else
    echo "plug: could not install the setuid helper — meanwhile run 'sudo plug <command>', or fix with:" >&2
    echo "      sudo chown root:wheel '$dest/plug' && sudo chmod u+s '$dest/plug'" >&2
  fi
fi
case ":$PATH:" in *":$dest:"*) : ;; *) echo "plug: add $dest to your PATH" ;; esac
if [ -n "$_profile" ]; then
  echo "plug: profile $_profile"
fi
# A hosted cluster lets in only the keys it knows: without this step, the
# first run is refused. Said with the profile it applies to, and where to
# register the key, in the gateway's words when it gave them.
if [ -n "$_hosted" ]; then
  _kp=${_pname:-<profile>}
  echo "plug: this cluster lets in only the keys it knows. Give this machine its own, once:"
  echo "        plug keygen -p $_kp"
  echo "        plug pubkey -p $_kp"
  echo "      and ${_keyhint:-give the key it prints to whoever operates the cluster}"
fi
if [ -n "$_profile" ]; then
  echo "plug: then run 'plug -p $_pname <your command>'  (the only profile? 'plug <your command>' is enough)"
else
  echo "plug: run 'plug <your command>'; the first run asks for your cluster"
fi
