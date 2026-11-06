#!/bin/sh
set -eu
for node in mariadb mariadb-2 mariadb-3; do
    docker compose -f compose.app.yaml exec -T "$node" /opt/galera/healthy.sh
    size=$(docker compose -f compose.app.yaml exec -T "$node" sh -ec 'export MYSQL_PWD="$(cat /run/secrets/mariadb_root_password)"; mariadb -uroot -NBe "SHOW GLOBAL STATUS LIKE '\''wsrep_cluster_size'\''"' | awk '{print $2}')
    [ "$size" = 3 ] || { echo "$node: expected cluster size 3, got $size" >&2; exit 1; }
    echo "PASS: $node Primary/Synced/Ready; cluster size 3"
done
