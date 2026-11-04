#!/usr/bin/env bash
set -euo pipefail
cd -- "$(dirname -- "$0")/.."

command -v openssl >/dev/null
docker compose -f compose.app.yaml exec -T mariadb healthcheck.sh --connect --innodb_initialized

umask 077
mkdir -p .secrets/monitoring
chmod 700 .secrets/monitoring
if [[ ! -f .secrets/monitoring/exporter_password ]]; then
  openssl rand -hex 32 > .secrets/monitoring/exporter_password
fi
if [[ ! -f .secrets/monitoring/grafana_admin_password ]]; then
  openssl rand -hex 32 > .secrets/monitoring/grafana_admin_password
fi
exporter_password=$(<.secrets/monitoring/exporter_password)
if [[ ! $exporter_password =~ ^[0-9a-f]{64}$ ]]; then
  echo 'Exporter password must contain 64 lowercase hex characters.' >&2
  exit 1
fi

# Root credentials remain inside MariaDB; SQL is sent on stdin, not argv.
docker compose -f compose.app.yaml exec -T mariadb sh -ec \
  'export MYSQL_PWD="$(cat /run/secrets/mariadb_root_password)"; exec mariadb --user=root' <<SQL
CREATE USER IF NOT EXISTS 'monitoring'@'%' IDENTIFIED BY '${exporter_password}' WITH MAX_USER_CONNECTIONS 3;
ALTER USER 'monitoring'@'%' IDENTIFIED BY '${exporter_password}' WITH MAX_USER_CONNECTIONS 3;
GRANT PROCESS, REPLICATION CLIENT, SELECT ON *.* TO 'monitoring'@'%';
SQL

printf '[client]\nuser=monitoring\npassword=%s\n' "$exporter_password" > .secrets/monitoring/mysqld_exporter.cnf
# Compose bind-mounts local secret files preserving host permissions. The
# enclosing 0700 directory restricts host access; non-root containers need read.
chmod 644 .secrets/monitoring/mysqld_exporter.cnf .secrets/monitoring/grafana_admin_password
echo 'Monitoring credentials prepared; values not printed.'
