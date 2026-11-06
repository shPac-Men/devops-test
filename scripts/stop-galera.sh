#!/bin/sh
set -eu
cd -- "$(dirname -- "$0")/.."
docker compose -f compose.app.yaml stop uptime-kuma haproxy
# Stop sequentially: the last member records safe_to_bootstrap=1.
for node in mariadb-3 mariadb-2 mariadb; do
    docker compose -f compose.app.yaml stop -t 60 "$node"
done
