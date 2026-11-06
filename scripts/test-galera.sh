#!/bin/sh
# Integration test: replicated writes and failover through HAProxy.
set -eu
cd -- "$(dirname -- "$0")/.."
compose() { docker compose -f compose.app.yaml "$@"; }
query() {
    node=$1; host=$2; statement=$3
    limit=${4:-10}
    timeout -k 2 "$limit" docker compose -f compose.app.yaml exec -T "$node" timeout "$limit" sh -ec 'export MYSQL_PWD="$(cat /run/secrets/kuma_db_password)"; exec mariadb --connect-timeout=5 -h "$1" -uuptime_kuma -NBe "$2" uptime_kuma' sh "$host" "$statement"
}
wait_write() {
    deadline=$(($(date +%s) + 120))
    while :; do
        remaining=$((deadline - $(date +%s)))
        [ "$remaining" -gt 0 ] || { echo 'Write through HAProxy timed out after 120s' >&2; return 1; }
        [ "$remaining" -le 10 ] || remaining=10
        if query mariadb-2 haproxy "$1" "$remaining"; then return 0; fi
        sleep 1
    done
}
sh scripts/check-galera.sh
# A temporary table name is unique to this test invocation.
table="galera_probe_$(date +%s)_$$"
modified=0
cleanup() {
    if [ "$modified" = 1 ]; then compose start mariadb; fi
    compose up -d --wait --wait-timeout 300 mariadb
    query mariadb-2 haproxy "DROP TABLE IF EXISTS $table"
}
trap cleanup EXIT
trap 'exit 1' INT TERM
query mariadb-2 haproxy "CREATE TABLE $table (id INT PRIMARY KEY, value VARCHAR(32)) ENGINE=InnoDB; INSERT INTO $table VALUES (1,'before-failover')"
modified=1
compose stop -t 60 mariadb
wait_write "INSERT INTO $table VALUES (2,'during-failover') ON DUPLICATE KEY UPDATE value=VALUES(value)"
for node in mariadb-2 mariadb-3; do
    count=$(query "$node" "$node" "SELECT COUNT(*) FROM $table")
    [ "$count" = 2 ] || { echo "$node: replication mismatch" >&2; exit 1; }
done
compose start mariadb
compose up -d --wait --wait-timeout 300 mariadb
modified=0
sh scripts/check-galera.sh
count=$(query mariadb mariadb "SELECT COUNT(*) FROM $table")
[ "$count" = 2 ] || { echo 'Recovered node lost test writes' >&2; exit 1; }
echo 'PASS: HAProxy accepted a write with node 1 stopped; both surviving nodes and the rejoined node contain both rows.'
