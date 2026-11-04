#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
if [[ -f .env ]]; then set -a; source .env; set +a; fi
dagger_bin="${DAGGER_BIN:-dagger}"
if [[ -x .tools/dagger && -z "${DAGGER_BIN:-}" ]]; then dagger_bin=.tools/dagger; fi
if ! command -v "$dagger_bin" >/dev/null; then
 echo 'Dagger CLI 0.21.11 is required. See README.md.' >&2; exit 1
fi
if [[ "$($dagger_bin version)" != 'dagger v0.21.11 '* ]]; then
 echo 'Use Dagger CLI 0.21.11 to match dagger.json.' >&2; exit 1
fi
for name in root kuma exporter grafana; do
 [[ -f ".secrets/$name-password" ]] || { echo 'Run python3 scripts/init-secrets.py first.' >&2; exit 1; }
done
if [[ ! -S "${DOCKER_SOCKET:-/var/run/docker.sock}" ]]; then
 echo "Docker socket not found: ${DOCKER_SOCKET:-/var/run/docker.sock}" >&2; exit 1
fi
exec "$dagger_bin" call \
 --docker-socket="${DOCKER_SOCKET:-/var/run/docker.sock}" \
 --root-password=file://.secrets/root-password \
 --kuma-password=file://.secrets/kuma-password \
 --exporter-password=file://.secrets/exporter-password \
 --grafana-password=file://.secrets/grafana-password \
 --db-host="${DB_HOST:-mariadb}" \
 --kuma-port="${KUMA_PORT:-3001}" \
 --grafana-port="${GRAFANA_PORT:-3000}" \
 --prometheus-port="${PROMETHEUS_PORT:-9090}" "$@"
