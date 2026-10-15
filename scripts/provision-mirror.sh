#!/usr/bin/env bash
# Explicit destructive provisioning tool. Never called by the daemon or installer.
set -euo pipefail
if [[ $# != 5 || "$1" != "--erase-confirmed" ]]; then
  echo "Usage: $0 --erase-confirmed /dev/nvme0n1 EXPECTED_SERIAL /dev/nvme1n1 EXPECTED_SERIAL" >&2
  exit 2
fi
[[ $EUID == 0 ]] || { echo 'Run as root' >&2; exit 1; }
[[ -s /etc/hostid ]] || zgenhostid
a="$2"; a_serial="$3"; b="$4"; b_serial="$5"
[[ "$a" != "$b" && "$a" =~ ^/dev/nvme[0-9]+n[0-9]+$ && "$b" =~ ^/dev/nvme[0-9]+n[0-9]+$ ]] || exit 2
if zpool list -H backup-pool >/dev/null 2>&1; then echo 'backup-pool already exists; refusing to overwrite' >&2; exit 1; fi
for device in "$a" "$b"; do
  [[ -b "$device" ]] || exit 1
  expected="$a_serial"; [[ "$device" == "$b" ]] && expected="$b_serial"
  actual="$(lsblk -dn -o SERIAL "$device" | xargs)"
  [[ "$actual" == "$expected" ]] || { echo "Serial mismatch for $device" >&2; exit 1; }
  [[ -z "$(lsblk -nr -o MOUNTPOINTS "$device" | sed '/^[[:space:]]*$/d')" ]] || { echo "Mounted device: $device" >&2; exit 1; }
done
# Only legacy Ceph LVs located on the explicitly authorized disks may be removed.
for device in "$a" "$b"; do
  while read -r vg; do
    [[ -z "$vg" ]] && continue
    [[ "$vg" == ceph-* ]] || { echo "Non-Ceph volume group on $device; refusing" >&2; exit 1; }
    [[ "$(pvs --noheadings -o pv_count --select vg_name="$vg" 2>/dev/null | xargs)" == "1" ]] || { echo 'Shared VG; refusing' >&2; exit 1; }
    vgremove -y "$vg"
    pvremove -y "$device"
  done < <(pvs --noheadings -o vg_name "$device" 2>/dev/null | xargs -n1 || true)
  zpool labelclear -f "$device" 2>/dev/null || true
  wipefs --all "$device"
done
persistent_path() {
  local device="$1" link
  for link in /dev/disk/by-id/nvme-eui.* /dev/disk/by-id/nvme-*; do
    [[ -e "$link" && "$(readlink -f "$link")" == "$device" ]] && { printf '%s' "$link"; return; }
  done
  echo "No persistent disk ID for $device" >&2; return 1
}
a_path="$(persistent_path "$a")"
b_path="$(persistent_path "$b")"
zpool create -f -o ashift=12 -o autotrim=on -o cachefile=/etc/zfs/zpool.cache \
  -O mountpoint=none -O compression=lz4 -O atime=off -O xattr=sa -O acltype=posixacl -O sync=standard \
  backup-pool mirror "$a_path" "$b_path"
zpool status backup-pool
