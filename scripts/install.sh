#!/usr/bin/env bash
# Install a built release on a host with an existing ZFS pool. Does not erase disks.
set -euo pipefail
[[ $EUID == 0 ]] || { echo 'Run as root' >&2; exit 1; }
cd "$(dirname "$0")/.."
command -v zfs >/dev/null
zpool list -H backup-pool >/dev/null
for cmd in nginx exportfs targetcli smartctl rsync rclone sshpass; do command -v "$cmd" >/dev/null || { echo "Missing dependency: $cmd" >&2; exit 1; }; done
install -d -m 0755 /opt/backup-center /opt/backup-center/web /etc/backup-center /etc/exports.d /etc/target
install -d -m 0700 /var/lib/backup-center /etc/backup-center/tls
if [[ -f /opt/backup-center/backupd ]]; then cp -a /opt/backup-center/backupd /opt/backup-center/backupd.previous; fi
systemctl stop backupd.service 2>/dev/null || true
install -m 0755 bin/backupd /opt/backup-center/backupd
install -m 0755 scripts/export-pool.sh /opt/backup-center/export-pool.sh
# Clear only prior frontend release files, never backup data.
find /opt/backup-center/web -mindepth 1 -maxdepth 1 -exec rm -rf -- {} +
cp -a web/dist/. /opt/backup-center/web/
chmod -R a+rX /opt/backup-center/web
if [[ ! -f /etc/backup-center/tls/server.key ]]; then
  openssl req -x509 -newkey rsa:3072 -sha256 -nodes -days 365 \
    -keyout /etc/backup-center/tls/server.key -out /etc/backup-center/tls/server.crt \
    -subj '/CN=backup.example.com/O=Backup Center' \
    -addext 'subjectAltName=IP:192.0.2.10,IP:192.0.2.13,DNS:backup.example.com' \
    -addext 'extendedKeyUsage=serverAuth'
  chmod 0600 /etc/backup-center/tls/server.key
fi
chmod 0644 /etc/backup-center/tls/server.crt
install -m 0644 deploy/backupd.service /etc/systemd/system/backupd.service
install -m 0644 deploy/backup-center-scrub.service /etc/systemd/system/
install -m 0644 deploy/backup-center-scrub.timer /etc/systemd/system/
install -m 0644 deploy/backup-center-load-keys.service /etc/systemd/system/
install -m 0644 deploy/backup-center-pool-guard.service /etc/systemd/system/
install -m 0644 deploy/nginx.conf /etc/nginx/conf.d/backup-center.conf
if [[ ! -f /etc/nginx/nginx.conf.before-backup-center ]]; then cp -a /etc/nginx/nginx.conf /etc/nginx/nginx.conf.before-backup-center; fi
cat > /etc/nginx/nginx.conf <<'NGINX'
user nginx;
worker_processes auto;
worker_rlimit_nofile 65536;
error_log /var/log/nginx/error.log warn;
pid /run/nginx.pid;
include /usr/share/nginx/modules/*.conf;
events { worker_connections 2048; }
http {
  include /etc/nginx/mime.types;
  default_type application/octet-stream;
  sendfile on;
  keepalive_timeout 65;
  gzip on;
  gzip_types application/javascript text/css application/json;
  include /etc/nginx/conf.d/backup-center.conf;
}
NGINX
nginx -t
if [[ ! -s /var/lib/backup-center/backup-center.db ]]; then
  umask 077
  openssl rand -base64 30 > /etc/backup-center/initial-admin-password
  /opt/backup-center/backupd --init-admin admin --password-file /etc/backup-center/initial-admin-password
  echo 'Administrator initialized; password is in /etc/backup-center/initial-admin-password (root only).'
fi
# Pool GUID discovery and cached import survive device enumeration changes.
[[ -s /etc/hostid ]] || zgenhostid
printf 'zfs\n' > /etc/modules-load.d/backup-center-zfs.conf
systemctl daemon-reload
zpool set cachefile=/etc/zfs/zpool.cache backup-pool
systemctl enable zfs-import-cache.service zfs-mount.service zfs.target backup-center-load-keys.service backup-center-pool-guard.service
systemctl enable --now backupd.service nginx.service backup-center-scrub.timer
systemctl reload nginx
if command -v restorecon >/dev/null; then restorecon -R /opt/backup-center/web /etc/backup-center/tls || true; fi
curl -fsS --retry 10 --retry-connrefused --retry-delay 1 --max-time 10 http://127.0.0.1:8080/healthz
