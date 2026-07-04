#!/bin/sh
set -e
ssh-keygen -A

# If the admin set the agent's public address in the stack (PLUG_PUBLIC_HOST),
# record it so the installer can bake it into each dev's profile — no need for
# devs to repeat the cluster address.
if [ -n "$PLUG_PUBLIC_HOST" ]; then
  printf '%s %s\n' "$PLUG_PUBLIC_HOST" "${PLUG_PUBLIC_PORT:-2222}" > /opt/plug/public
fi

echo "plug agent ready (v$(cat /opt/plug/VERSION 2>/dev/null))"
exec /usr/sbin/sshd -D -e
