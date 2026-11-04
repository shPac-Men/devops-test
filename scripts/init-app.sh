#!/bin/sh
set -eu
cd -- "$(dirname -- "$0")/.."
umask 077
mkdir -p .secrets/app
chmod 700 .secrets/app
for name in kuma_db_password mariadb_root_password; do
  file=".secrets/app/$name"
  if [ ! -f "$file" ]; then
    value=$(od -An -tx1 -N32 /dev/urandom | tr -d ' \n')
    [ "${#value}" -eq 64 ] || { echo 'Password generation failed.' >&2; exit 1; }
    printf '%s\n' "$value" > "$file"
  fi
  [ -s "$file" ] || { echo "Empty password file: $file" >&2; exit 1; }
done
if [ "$(id -u)" -eq 0 ]; then
  owner=$(stat -c '%u:%g' .)
  chown "$owner" .secrets .secrets/app .secrets/app/kuma_db_password .secrets/app/mariadb_root_password
fi
echo 'Application credentials ready; values not printed.'
