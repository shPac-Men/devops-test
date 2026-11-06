#!/bin/sh
set -eu
cd -- "$(dirname -- "$0")/.."
if command -v dagger >/dev/null 2>&1; then
  dagger_bin=dagger
elif [ -x .tools/dagger ]; then
  dagger_bin=./.tools/dagger
else
  echo 'Install Dagger CLI v0.21.11 and make it available in PATH.' >&2
  exit 1
fi
exec "$dagger_bin" call "$@"
