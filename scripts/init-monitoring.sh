#!/bin/sh
set -eu
cd -- "$(dirname -- "$0")/.."

docker compose -f compose.app.yaml exec -T mariadb healthcheck.sh --connect --innodb_initialized

umask 077
mkdir -p .secrets/monitoring
chmod 700 .secrets/monitoring
if [ ! -f .secrets/monitoring/exporter_password ]; then
  od -An -tx1 -N32 /dev/urandom | tr -d ' \n' > .secrets/monitoring/exporter_password
fi
if [ ! -f .secrets/monitoring/grafana_admin_password ]; then
  od -An -tx1 -N32 /dev/urandom | tr -d ' \n' > .secrets/monitoring/grafana_admin_password
fi
exporter_password=$(cat .secrets/monitoring/exporter_password)
if [ "${#exporter_password}" -ne 64 ] || printf '%s' "$exporter_password" | grep -q '[^0-9a-f]'; then
  echo 'Exporter password must contain 64 lowercase hex characters.' >&2
  exit 1
fi
[ -s .secrets/monitoring/grafana_admin_password ] || { echo 'Grafana password file is empty.' >&2; exit 1; }

# Root credentials remain inside MariaDB; SQL is sent on stdin, not argv.
docker compose -f compose.app.yaml exec -T mariadb sh -ec \
  'export MYSQL_PWD="$(cat /run/secrets/mariadb_root_password)"; exec mariadb --user=root' <<SQL
CREATE USER IF NOT EXISTS 'monitoring'@'%' IDENTIFIED BY '${exporter_password}' WITH MAX_USER_CONNECTIONS 10;
ALTER USER 'monitoring'@'%' IDENTIFIED BY '${exporter_password}' WITH MAX_USER_CONNECTIONS 10;
GRANT PROCESS, REPLICATION CLIENT, SELECT ON *.* TO 'monitoring'@'%';
SQL

printf '[client]\nuser=monitoring\npassword=%s\n' "$exporter_password" > .secrets/monitoring/mysqld_exporter.cnf
# Compose bind-mounts local secret files preserving host permissions. The
# enclosing 0700 directory restricts host access; non-root containers need read.
chmod 644 .secrets/monitoring/mysqld_exporter.cnf .secrets/monitoring/grafana_admin_password
if [ "$(id -u)" -eq 0 ]; then
  owner=$(stat -c '%u:%g' .)
  chown "$owner" .secrets .secrets/monitoring .secrets/monitoring/exporter_password .secrets/monitoring/mysqld_exporter.cnf .secrets/monitoring/grafana_admin_password
fi
echo 'Monitoring credentials prepared; values not printed.'
