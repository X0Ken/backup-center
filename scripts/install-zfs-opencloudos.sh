#!/usr/bin/env bash
# Reproducible OpenZFS installation for the appliance's OpenCloudOS 9 kernel.
set -euo pipefail
[[ $EUID == 0 ]] || { echo 'Run as root' >&2; exit 1; }
. /etc/os-release
[[ "$ID" == opencloudos && "${VERSION_ID%%.*}" == 9 ]] || { echo 'OpenCloudOS 9 required' >&2; exit 1; }
version=2.4.4
checksum=2a3c70d55a37cc71618a95a60e81ad66530201eb118d37741dc92efcf848c8b1
kernel="$(uname -r)"
if command -v zfs >/dev/null && zfs version | head -1 | grep -qx "zfs-${version}-1" && dkms status | grep -F "zfs/$version, $kernel" | grep -q installed; then
  echo 'Pinned ZFS and current-kernel DKMS module already installed.'; exit 0
fi
if rpm -q zfs >/dev/null 2>&1; then echo 'A packaged ZFS installation exists; do not mix installation methods.' >&2; exit 1; fi
dnf -y install gcc make autoconf automake libtool libtirpc-devel libblkid-devel libuuid-devel \
  libudev-devel openssl-devel zlib-devel libaio-devel libattr-devel elfutils-libelf-devel \
  python3-devel python3-setuptools libffi-devel dkms "kernel-devel-$kernel" \
  nginx nfs-utils targetcli smartmontools nvme-cli rsync
[[ -s /etc/hostid ]] || zgenhostid 2>/dev/null || true
work=/var/cache/backup-center
mkdir -p "$work"
cd "$work"
curl -fL --retry 3 -o "zfs-$version.tar.gz" "https://github.com/openzfs/zfs/releases/download/zfs-$version/zfs-$version.tar.gz"
printf '%s  %s\n' "$checksum" "zfs-$version.tar.gz" | sha256sum --check --strict
tar -xzf "zfs-$version.tar.gz"
cd "zfs-$version"
./configure --prefix=/usr --sysconfdir=/etc --sbindir=/usr/sbin --libdir=/usr/lib64 \
  --with-linux="/lib/modules/$kernel/build" --with-linux-obj="/lib/modules/$kernel/build"
make -j"$(nproc)"
make install
ldconfig
tar -xzf "$work/zfs-$version.tar.gz" -C /usr/src
cd "/usr/src/zfs-$version"
sh scripts/dkms.mkconf -n zfs -v "$version" -f dkms.conf
dkms add -m zfs -v "$version"
dkms build -m zfs -v "$version" -k "$kernel" -j "$(nproc)"
dkms install -m zfs -v "$version" -k "$kernel" --force
depmod -a
modprobe zfs
[[ -s /etc/hostid ]] || zgenhostid
zfs version
dkms status
