#!/bin/sh
set -e
ssh-keygen -A
echo "plug agent ready (v$(cat /opt/plug/VERSION 2>/dev/null))"
exec /usr/sbin/sshd -D -e
