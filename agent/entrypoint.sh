#!/bin/sh
set -e
ssh-keygen -A
# plug is here to plug services into the cluster. Without the access that takes,
# -s cannot work at all — refuse to come up, rather than look healthy until the
# first session tries. `set -e` above turns this into a failed container.
/usr/local/bin/plug-agent preflight
# An agent (re)start orphans every session's dynamic name (docker signposts /
# k8s Services) — sweep them before serving.
/usr/local/bin/plug-agent gc 2>/dev/null || true
echo "plug agent ready (v$(cat /opt/plug/VERSION 2>/dev/null))"
exec /usr/sbin/sshd -D -e
