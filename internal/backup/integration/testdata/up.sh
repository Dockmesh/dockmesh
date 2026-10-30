#!/usr/bin/env bash
# Starts the test-only backup targets and prints the env the tests read.
set -euo pipefail
here="$(cd "$(dirname "$0")" && pwd)"
export IT_DIR="${IT_DIR:-$(mktemp -d)}"
mkdir -p "$IT_DIR/sftp"

ssh-keygen -q -t ed25519 -N '' -f "$IT_DIR/sftp/client_ed25519" -C client
printf 'backup:it-sftp-pass:1001:1001:upload\n' > "$IT_DIR/sftp/users.conf"

cd "$here"
docker compose up -d --build --wait --wait-timeout 120 smb sftp webdav garage 2>&1 | tail -n 5 >&2

for port in 1445 2222 8090; do
  for _ in $(seq 1 60); do (exec 3<>"/dev/tcp/127.0.0.1/$port") 2>/dev/null && break; sleep 1; done
done

garage() { docker compose exec -T garage /garage "$@"; }
for _ in $(seq 1 30); do garage status >/dev/null 2>&1 && break; sleep 1; done
node_id="$(garage node id -q 2>/dev/null | cut -d@ -f1)"
garage layout assign -z dc1 -c 1G "$node_id" >/dev/null
garage layout apply --version 1 >/dev/null
garage bucket create dm-it >/dev/null
key="$(garage key create dm-it-key)"
access="$(sed -n 's/^Key ID: *//p' <<<"$key")"
secret="$(sed -n 's/^Secret key: *//p' <<<"$key")"
[[ $access =~ ^[A-Za-z0-9]+$ && $secret =~ ^[A-Za-z0-9]+$ ]] || { echo "unexpected garage key output" >&2; exit 1; }
garage bucket allow --read --write --owner dm-it --key dm-it-key >/dev/null

{
  echo "DM_IT_SMB_ADDR=127.0.0.1:1445"
  echo "DM_IT_SMB_USER=backup"
  echo "DM_IT_SMB_PASS=it-smb-pass"
  echo "DM_IT_SMB_SHARE=backups"
  echo "DM_IT_SFTP_ADDR=127.0.0.1:2222"
  echo "DM_IT_SFTP_USER=backup"
  echo "DM_IT_SFTP_PASS=it-sftp-pass"
  echo "DM_IT_SFTP_KEY_FILE=$IT_DIR/sftp/client_ed25519"
  echo "DM_IT_WEBDAV_URL=http://127.0.0.1:8090"
  echo "DM_IT_WEBDAV_USER=backup"
  echo "DM_IT_WEBDAV_PASS=it-webdav-pass"
  echo "DM_IT_S3_ENDPOINT=127.0.0.1:3900"
  echo "DM_IT_S3_REGION=garage"
  echo "DM_IT_S3_BUCKET=dm-it"
  echo "DM_IT_S3_ACCESS_KEY=$access"
  echo "DM_IT_S3_SECRET_KEY=$secret"
}
