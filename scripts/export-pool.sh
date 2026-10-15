#!/usr/bin/env bash
# A normal zpool export removes the pool from zpool.cache. Preserve the verified
# cache at clean shutdown so cache-only boot import can still discover this pool.
set -euo pipefail
cache=/etc/zfs/zpool.cache
[[ -s "$cache" ]] || { echo 'Missing ZFS import cache; refusing export' >&2; exit 1; }
copy="$(mktemp /etc/zfs/.backup-center-cache.XXXXXX)"
trap 'rm -f -- "$copy"' EXIT
cp -p -- "$cache" "$copy"
/usr/sbin/zpool export backup-pool
mv -f -- "$copy" "$cache"
sync -f "$cache"
