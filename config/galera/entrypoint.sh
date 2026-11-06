#!/bin/bash
set -euo pipefail
# The probe checks wsrep state, not merely an open MySQL TCP port.
socat TCP-LISTEN:9200,reuseaddr,fork EXEC:/opt/galera/probe.sh &
args=(mariadbd "--wsrep-node-name=$HOSTNAME" "--wsrep-node-address=$(hostname -i | awk '{print $1}')")
# Consume BEFORE starting mysqld: a Docker restart must never bootstrap again.
if [ -f /var/lib/mysql/.galera-bootstrap ]; then
    rm /var/lib/mysql/.galera-bootstrap
    args+=(--wsrep-new-cluster)
fi
exec /usr/local/bin/docker-entrypoint.sh "${args[@]}"
