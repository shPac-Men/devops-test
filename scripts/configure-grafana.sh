#!/bin/sh
set -eu
cd -- "$(dirname -- "$0")/.."
[ -s .secrets/monitoring/grafana_admin_password ] || { echo 'Run deploy-monitoring first.' >&2; exit 1; }
umask 077
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT HUP INT TERM
# Keep credentials out of curl argv and Dagger logs. This temp file lives only
# in the short-lived host helper, outside the read-only project mount.
password=$(cat .secrets/monitoring/grafana_admin_password)
auth=$(printf 'admin:%s' "$password" | base64 | tr -d '\n')
printf 'header = "Authorization: Basic %s"\n' "$auth" > "$tmp/auth"
base=http://grafana:3000
request() {
  curl --silent --show-error --connect-timeout 5 --max-time 30 \
    --config "$tmp/auth" --header 'Content-Type: application/json' "$@"
}
request --fail "$base/api/health" > /dev/null
status=$(request --output "$tmp/response" --write-out '%{http_code}' "$base/api/datasources/uid/safeboard-prometheus")
case "$status" in
  200) request --fail-with-body --request PUT --data-binary @config/grafana/datasource.json "$base/api/datasources/uid/safeboard-prometheus" > /dev/null ;;
  404) request --fail-with-body --request POST --data-binary @config/grafana/datasource.json "$base/api/datasources" > /dev/null ;;
  *) echo "Grafana data source lookup failed: HTTP $status" >&2; exit 1 ;;
esac
request --fail-with-body "$base/api/datasources/uid/safeboard-prometheus/health" > /dev/null
request --fail-with-body --request POST --data-binary @config/grafana/dashboard.json "$base/api/dashboards/db" > /dev/null
request --fail-with-body "$base/api/dashboards/uid/safeboard-health" > /dev/null
echo 'Grafana data source and dashboard applied: http://127.0.0.1:3000/d/safeboard-health'
