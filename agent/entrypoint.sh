#!/bin/sh
set -e
ssh-keygen -A
echo "plug agent ready — attached networks:"
ip -o -4 addr show | awk '{print "  " $2 " " $4}'
exec /usr/sbin/sshd -D -e
