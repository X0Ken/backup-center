#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
[[ -x bin/backupd && -s web/dist/index.html ]] || { echo 'Run scripts/build.sh first' >&2; exit 1; }
archive="bin/backup-center-1.1.0-linux-amd64.tar.gz"
tar -czf "$archive" bin/backupd web/dist deploy scripts/install.sh scripts/export-pool.sh README.md
sha256sum "$archive" > "$archive.sha256"
printf 'Release: %s\n' "$archive"
