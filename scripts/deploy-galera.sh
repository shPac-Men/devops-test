#!/bin/sh
set -eu
cd -- "$(dirname -- "$0")/.."
compose() { docker compose -f compose.app.yaml "$@"; }
sql() { compose exec -T "$1" sh -ec 'export MYSQL_PWD="$(cat /run/secrets/mariadb_root_password)"; exec mariadb -uroot -NBe "$1"' sh "$2"; }
sh scripts/init-app.sh
compose config --quiet
# Preserve a logical backup before converting the existing standalone database.
if [ "$(sql mariadb 'SELECT @@wsrep_on' 2>/dev/null || true)" = 0 ]; then
    compose stop uptime-kuma
    umask 077
    mkdir -p .secrets/backups
    backup=".secrets/backups/pre-galera-$(date +%Y%m%d%H%M%S).sql"
    compose exec -T mariadb sh -ec 'export MYSQL_PWD="$(cat /run/secrets/mariadb_root_password)"; exec mariadb-dump -uroot --single-transaction --databases uptime_kuma' > "$backup"
    if [ "$(id -u)" -eq 0 ]; then chown "$(stat -c '%u:%g' .)" .secrets/backups "$backup"; fi
    compose stop mariadb
fi
primary=''
for node in mariadb mariadb-2 mariadb-3; do
    if compose exec -T "$node" /opt/galera/healthy.sh >/dev/null 2>&1; then primary="$node"; break; fi
done
if [ -z "$primary" ]; then
    # Never create a second primary while another database process is running.
    if [ -n "$(compose ps --status running -q mariadb mariadb-2 mariadb-3)" ]; then
        echo 'No ready Primary, but DB containers are running. Refusing bootstrap. See README recovery.' >&2
        exit 1
    fi
    candidate=''
    initial=''
    existing_galera=0
    safe_count=0
    for node in mariadb mariadb-2 mariadb-3; do
        state=$(compose run --rm --no-deps --entrypoint sh "$node" -ec '
            if [ -f /var/lib/mysql/grastate.dat ]; then
                awk "/safe_to_bootstrap:/ {print \$2}" /var/lib/mysql/grastate.dat
            elif [ -d /var/lib/mysql/mysql ]; then echo legacy; else echo empty; fi')
        case "$state" in
            1) candidate="$node"; existing_galera=1; safe_count=$((safe_count+1)) ;;
            0) existing_galera=1 ;;
            legacy)
                [ "$node" = mariadb ] || { echo "Unexpected standalone data on $node; refusing bootstrap." >&2; exit 1; }
                initial=mariadb ;;
            empty) [ "$node" != mariadb ] || initial=mariadb ;;
            *) echo "Invalid grastate.dat on $node; refusing bootstrap." >&2; exit 1 ;;
        esac
    done
    [ "$safe_count" -le 1 ] || { echo "Multiple safe_to_bootstrap candidates; manual history check required." >&2; exit 1; }
    if [ -z "$candidate" ] && [ "$existing_galera" = 0 ]; then candidate="$initial"; fi
    [ -n "$candidate" ] || { echo 'No safe_to_bootstrap node. Manual crash recovery required; see README.' >&2; exit 1; }
    compose run --rm --no-deps --entrypoint sh "$candidate" -ec 'touch /var/lib/mysql/.galera-bootstrap; chown mysql:mysql /var/lib/mysql/.galera-bootstrap'
    compose up -d --wait --wait-timeout 300 "$candidate"
fi
# Join sequentially so SST donors are ready, then start the proxy and application.
for node in mariadb mariadb-2 mariadb-3; do
    compose up -d --wait --wait-timeout 300 "$node"
done
compose up -d --wait --wait-timeout 300
sh scripts/check-galera.sh
compose ps
