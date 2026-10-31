#!/bin/sh
set -eu
: "${SMB_USER:?}" "${SMB_PASS:?}" "${SMB_UID:=1002}"
addgroup -g "$SMB_UID" "$SMB_USER"
adduser -D -H -s /sbin/nologin -u "$SMB_UID" -G "$SMB_USER" "$SMB_USER"
mkdir -p /data
chown "$SMB_USER:$SMB_USER" /data
printf '%s\n%s\n' "$SMB_PASS" "$SMB_PASS" | smbpasswd -s -a "$SMB_USER" >/dev/null
exec smbd --foreground --no-process-group --debug-stdout
