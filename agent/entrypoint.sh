#!/bin/sh
set -e
ssh-keygen -A
# An agent (re)start orphans every session's dynamic name (docker signposts /
# k8s Services) — sweep them before serving.
/usr/local/bin/plug-agent gc 2>/dev/null || true
echo "plug agent ready (v$(cat /opt/plug/VERSION 2>/dev/null))"
exec /usr/sbin/sshd -D -e
