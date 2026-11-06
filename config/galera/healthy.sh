#!/bin/bash
set -euo pipefail
export MYSQL_PWD="$(cat /run/secrets/mariadb_root_password)"
state=$(mariadb --user=root --batch --skip-column-names --connect-timeout=3 -e "SHOW GLOBAL STATUS WHERE Variable_name IN ('wsrep_ready','wsrep_connected','wsrep_local_state','wsrep_cluster_status')")
grep -q $'wsrep_ready\tON' <<< "$state"
grep -q $'wsrep_connected\tON' <<< "$state"
grep -q $'wsrep_local_state\t4' <<< "$state"
grep -q $'wsrep_cluster_status\tPrimary' <<< "$state"
