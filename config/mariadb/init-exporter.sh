# Sourced by the official MariaDB entrypoint on an empty data volume.
exporter_password="$(cat /run/exporter/password)"
case "$exporter_password" in *[!0-9a-f]*|'') echo 'Invalid exporter secret' >&2; exit 1;; esac
docker_process_sql <<EOSQL
CREATE USER IF NOT EXISTS 'exporter'@'%' IDENTIFIED BY '${exporter_password}' WITH MAX_USER_CONNECTIONS 3;
GRANT PROCESS, REPLICATION CLIENT, SELECT ON *.* TO 'exporter'@'%';
EOSQL
unset exporter_password
