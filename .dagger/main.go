// Deploy the local Uptime Kuma platform through the host Docker daemon.
package main

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"dagger/safeboard/internal/dagger"
)

const dockerImage = "docker:28.5.1-cli"
const curlImage = "curlimages/curl:8.17.0"

type Safeboard struct {
	ProjectDir   string
	DockerSocket *dagger.Socket
}

func New(
	// Absolute project path on the Docker host (not inside the Dagger engine).
	projectDir string,
	// Host Docker socket; normally /var/run/docker.sock.
	dockerSocket *dagger.Socket,
) *Safeboard {
	return &Safeboard{ProjectDir: projectDir, DockerSocket: dockerSocket}
}

// Deploy MariaDB and Uptime Kuma, preserving credentials and data volumes.
// +cache="never"
func (m *Safeboard) DeployApp(ctx context.Context) (string, error) {
	return m.run(ctx, dockerImage, "", false, `
sh scripts/init-app.sh
docker compose -f compose.app.yaml config --quiet
docker compose -f compose.app.yaml up -d --wait --wait-timeout 300
docker compose -f compose.app.yaml ps
`)
}

// Deploy monitoring after deploy-app, including its database monitoring user.
// +cache="never"
func (m *Safeboard) DeployMonitoring(ctx context.Context) (string, error) {
	return m.run(ctx, dockerImage, "", false, `
docker network inspect safeboard-app_default >/dev/null 2>&1 || { echo 'Run deploy-app first.' >&2; exit 1; }
sh scripts/init-monitoring.sh
docker compose -f compose.monitoring.yaml config --quiet
docker compose -f compose.monitoring.yaml run --rm --no-deps --entrypoint promtool prometheus check config /etc/prometheus/prometheus.yml
docker compose -f compose.monitoring.yaml up -d --wait --wait-timeout 180
docker compose -f compose.monitoring.yaml ps
`)
}

// Create or update the Prometheus data source and platform dashboard via Grafana API.
// +cache="never"
func (m *Safeboard) ConfigureGrafana(ctx context.Context) (string, error) {
	return m.run(ctx, curlImage, "safeboard-monitoring_default", true, "sh scripts/configure-grafana.sh")
}

// Deploy the application, monitoring and Grafana configuration in order.
// +cache="never"
func (m *Safeboard) DeployAll(ctx context.Context) (string, error) {
	steps := []struct {
		name string
		run  func(context.Context) (string, error)
	}{
		{"deploy-app", m.DeployApp},
		{"deploy-monitoring", m.DeployMonitoring},
		{"configure-grafana", m.ConfigureGrafana},
	}
	var output strings.Builder
	for i, step := range steps {
		result, err := step.run(ctx)
		if err != nil {
			return "", fmt.Errorf("deploy-all: step %d/%d (%s) failed; deployment stopped at this step: %w", i+1, len(steps), step.name, err)
		}
		fmt.Fprintf(&output, "Step %d/%d (%s): OK\n%s\n", i+1, len(steps), step.name, result)
	}
	return output.String(), nil
}

// Test dashboard failure/recovery and automatic process-crash restart for one service.
// +cache="never"
func (m *Safeboard) ChaosTest(
	ctx context.Context,
	// Compose service to test: mariadb or uptime-kuma.
	target string,
	// Timeout in seconds for each observation/recovery phase.
	// +default=120
	timeout int,
) (string, error) {
	if target != "mariadb" && target != "uptime-kuma" {
		return "", fmt.Errorf("chaos-test: target must be mariadb or uptime-kuma")
	}
	if timeout < 30 || timeout > 600 {
		return "", fmt.Errorf("chaos-test: timeout must be between 30 and 600 seconds")
	}
	result, err := m.runHelper(ctx, "python:3.14.0-alpine", "host", true, true, true,
		fmt.Sprintf("python3 -u scripts/chaos-test.py --target=%s --timeout=%d", target, timeout))
	if err != nil {
		return "", fmt.Errorf("chaos-test (%s) failed: %w", target, err)
	}
	return result, nil
}

// A host-side helper mounts the project at the same absolute path, so Compose
// bind mounts resolve on the Docker daemon's filesystem. Secrets never enter
// Dagger's Directory snapshots or image layers.
func (m *Safeboard) run(ctx context.Context, image, network string, readOnly bool, script string) (string, error) {
	return m.runHelper(ctx, image, network, readOnly, network == "", false, script)
}

func (m *Safeboard) runHelper(ctx context.Context, image, network string, readOnly, withSocket, hostPID bool, script string) (string, error) {
	if !filepath.IsAbs(m.ProjectDir) || strings.ContainsAny(m.ProjectDir, ",\r\n") {
		return "", fmt.Errorf("project-dir must be an absolute host path without commas or newlines")
	}
	projectDir := filepath.Clean(m.ProjectDir)
	mount := "type=bind,src=" + projectDir + ",dst=" + projectDir
	if readOnly {
		mount += ",readonly"
	}
	args := []string{"docker", "run", "--rm", "--user", "0:0", "--mount", mount, "--workdir", projectDir}
	if network != "" {
		args = append(args, "--network", network)
	}
	if withSocket {
		args = append(args, "--mount", "type=bind,src=/var/run/docker.sock,dst=/var/run/docker.sock")
	}
	if hostPID {
		args = append(args, "--pid", "host")
	}
	args = append(args, "--entrypoint", "sh", image, "-ec", script)
	return dag.Container().From(dockerImage).
		WithUnixSocket("/var/run/docker.sock", m.DockerSocket).
		// Function cache and execution-layer cache are separate in Dagger.
		WithEnvVariable("DEPLOY_RUN_ID", strconv.FormatInt(time.Now().UnixNano(), 10)).
		WithExec(args).Stdout(ctx)
}
