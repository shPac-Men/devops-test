// Local Uptime Kuma and monitoring lifecycle on the host Docker daemon.
package main

import (
	"context"
	"crypto/rand"
	"dagger/safeboard/internal/dagger"
	"encoding/hex"
	"fmt"
)

type Safeboard struct {
	Source           *dagger.Directory
	DockerSocket     *dagger.Socket
	RootPassword     *dagger.Secret
	KumaPassword     *dagger.Secret
	ExporterPassword *dagger.Secret
	GrafanaPassword  *dagger.Secret
	DBHost           string
	KumaPort         string
	GrafanaPort      string
	PrometheusPort   string
}

func New(
	// +defaultPath="."
	// +ignore=[".git", ".env", ".secrets", ".tools", "dist", "build", "devops-test", "*.docx", ".~lock.*", ".agents", ".aws", ".codex", ".dagger"]
	source *dagger.Directory,
	dockerSocket *dagger.Socket,
	rootPassword *dagger.Secret,
	kumaPassword *dagger.Secret,
	exporterPassword *dagger.Secret,
	grafanaPassword *dagger.Secret,
	// +default="mariadb"
	dbHost string,
	// +default="3001"
	kumaPort string,
	// +default="3000"
	grafanaPort string,
	// +default="9090"
	prometheusPort string,
) *Safeboard {
	return &Safeboard{source, dockerSocket, rootPassword, kumaPassword, exporterPassword, grafanaPassword, dbHost, kumaPort, grafanaPort, prometheusPort}
}

func (m *Safeboard) run(ctx context.Context, command, target, mode string) (string, error) {
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", err
	}
	id := hex.EncodeToString(nonce[:])
	binary := dag.Container().From("golang:1.26.8-alpine3.23").
		WithDirectory("/src", m.Source).WithWorkdir("/src").
		WithEnvVariable("CGO_ENABLED", "0").WithEnvVariable("GOOS", "linux").
		WithExec([]string{"go", "build", "-trimpath", "-o", "/platform", "./cmd/platform"}).File("/platform")
	client := dag.Container().From("docker:28.5.1-cli").
		WithUnixSocket("/var/run/docker.sock", m.DockerSocket).
		WithDirectory("/workspace", m.Source).WithFile("/platform", binary).
		WithMountedSecret("/input-root-password", m.RootPassword).
		WithMountedSecret("/input-kuma-password", m.KumaPassword).
		WithMountedSecret("/input-exporter-password", m.ExporterPassword).
		WithMountedSecret("/input-grafana-password", m.GrafanaPassword).
		WithEnvVariable("RUN_ID", id).WithEnvVariable("OPERATION", command).
		WithEnvVariable("TARGET", target).WithEnvVariable("MODE", mode).
		WithEnvVariable("DB_HOST", m.DBHost).WithEnvVariable("KUMA_PORT", m.KumaPort).
		WithEnvVariable("GRAFANA_PORT", m.GrafanaPort).WithEnvVariable("PROMETHEUS_PORT", m.PrometheusPort)
	return client.WithExec([]string{"sh", "-ec", controllerScript}).Stdout(ctx)
}

const controllerScript = `
docker info >/dev/null
docker network inspect safeboard >/dev/null 2>&1 || docker network create safeboard >/dev/null
for name in config root-secret kuma-secret exporter-secret grafana-secret; do
 docker volume create "safeboard-$name" >/dev/null
done
name="safeboard-controller-$RUN_ID"
cleanup() {
 docker stop --time 210 "$name" >/dev/null 2>&1 || true
 docker rm -f "$name" >/dev/null 2>&1 || true
}
trap cleanup EXIT INT TERM
docker create --name "$name" --network safeboard \
 --label safeboard.controller=true \
 -v /var/run/docker.sock:/var/run/docker.sock \
 -v safeboard-config:/staged-config \
 -v safeboard-root-secret:/secrets/root \
 -v safeboard-kuma-secret:/secrets/kuma \
 -v safeboard-exporter-secret:/secrets/exporter \
 -v safeboard-grafana-secret:/secrets/grafana \
 -e DB_HOST -e KUMA_PORT -e GRAFANA_PORT -e PROMETHEUS_PORT \
 --entrypoint /platform docker:28.5.1-cli "$OPERATION" "$TARGET" "$MODE" >/dev/null
docker cp /platform "$name:/platform"
docker cp /workspace "$name:/work"
for secret in root kuma exporter grafana; do
 docker cp "/input-$secret-password" "$name:/input-$secret-password"
done
docker start --attach "$name"
code=$(docker inspect -f '{{.State.ExitCode}}' "$name")
exit "$code"
`

// Start MariaDB and Uptime Kuma and wait for readiness.
// +cache="never"
func (m *Safeboard) DeployApp(ctx context.Context) (string, error) {
	return m.run(ctx, "deploy-app", "", "")
}

// Start the monitoring layer independently of the application.
// +cache="never"
func (m *Safeboard) DeployMonitoring(ctx context.Context) (string, error) {
	return m.run(ctx, "deploy-monitoring", "", "")
}

// Upsert the Prometheus data source and dashboard through Grafana HTTP API.
// +cache="never"
func (m *Safeboard) ConfigureGrafana(ctx context.Context) (string, error) {
	return m.run(ctx, "configure-grafana", "", "")
}

// Deploy both layers, configure Grafana, and validate all systems.
// +cache="never"
func (m *Safeboard) DeployAll(ctx context.Context) (string, error) {
	return m.run(ctx, "deploy-all", "", "")
}

// Validate containers, scrape targets, recorded health and dashboard queries.
// +cache="never"
func (m *Safeboard) Verify(ctx context.Context) (string, error) { return m.run(ctx, "verify", "", "") }

// Exercise stop/start and/or SIGKILL recovery, with cleanup on failure.
// +cache="never"
func (m *Safeboard) ChaosTest(ctx context.Context,
	// +default="kuma"
	target string,
	// +default="both"
	mode string,
) (string, error) {
	if target != "kuma" && target != "mariadb" {
		return "", fmt.Errorf("target must be kuma or mariadb")
	}
	if mode != "both" && mode != "stop" && mode != "crash" {
		return "", fmt.Errorf("mode must be both, stop or crash")
	}
	return m.run(ctx, "chaos-test", target, mode)
}
