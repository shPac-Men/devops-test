package platform

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const workdir = "/work"
const readyTimeout = 180 * time.Second

var secretPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
var hostnamePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9.-]*$`)

type runner struct {
	api     *API
	secrets []string
}

func Run(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("operation required")
	}
	command := args[0]
	switch command {
	case "deploy-app", "deploy-monitoring", "configure-grafana", "deploy-all", "verify", "chaos-test":
	default:
		return fmt.Errorf("unknown operation %q", command)
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Minute)
	defer cancel()
	if err := validateSettings(); err != nil {
		return err
	}
	r := &runner{api: &API{Client: &http.Client{Timeout: 5 * time.Second}, Grafana: "http://grafana:3000", Prometheus: "http://prometheus:9090"}}
	if err := r.stageSecrets(); err != nil {
		return err
	}
	if command == "deploy-app" || command == "deploy-monitoring" || command == "deploy-all" {
		if err := copyTree(filepath.Join(workdir, "config"), "/staged-config"); err != nil {
			return err
		}
	}
	var err error
	switch command {
	case "deploy-app":
		err = r.deployApp(ctx)
	case "deploy-monitoring":
		err = r.deployMonitoring(ctx)
	case "configure-grafana":
		err = r.configure(ctx)
	case "verify":
		err = r.verify(ctx)
	case "deploy-all":
		for _, phase := range []func(context.Context) error{r.deployApp, r.deployMonitoring, r.configure, r.verify} {
			if err = phase(ctx); err != nil {
				break
			}
		}
	case "chaos-test":
		target, mode := "kuma", "both"
		if len(args) > 1 && args[1] != "" {
			target = args[1]
		}
		if len(args) > 2 && args[2] != "" {
			mode = args[2]
		}
		err = r.chaos(ctx, target, mode)
	}
	if err != nil {
		// Status only: application logs and `compose config` may include secrets.
		diagnosticCtx, done := context.WithTimeout(context.Background(), 10*time.Second)
		defer done()
		for _, layer := range []string{"app", "monitoring"} {
			if out, e := r.compose(diagnosticCtx, layer, "ps", "--all"); e == nil {
				fmt.Print(out)
			}
		}
		return errors.New(r.redact(err.Error()))
	}
	fmt.Printf("OK: %s\n", command)
	return nil
}

func validateSettings() error {
	if host := os.Getenv("DB_HOST"); host != "" && (!hostnamePattern.MatchString(host) || len(host) > 253) {
		return errors.New("invalid DB_HOST")
	}
	used := map[int]bool{}
	for _, name := range []string{"KUMA_PORT", "GRAFANA_PORT", "PROMETHEUS_PORT"} {
		value := os.Getenv(name)
		if value == "" {
			continue
		}
		port, err := strconv.Atoi(value)
		if err != nil || port < 1024 || port > 65535 {
			return fmt.Errorf("%s must be a port between 1024 and 65535", name)
		}
		if used[port] {
			return errors.New("published ports must be distinct")
		}
		used[port] = true
	}
	return nil
}

func (r *runner) stageSecrets() error {
	for _, name := range []string{"root", "kuma", "exporter", "grafana"} {
		input, err := os.ReadFile("/input-" + name + "-password")
		if err != nil {
			return fmt.Errorf("read %s secret: %w", name, err)
		}
		password := strings.TrimSpace(string(input))
		if !secretPattern.MatchString(password) {
			return fmt.Errorf("%s secret must contain 64 lowercase hex characters; run scripts/init-secrets.py", name)
		}
		r.secrets = append(r.secrets, password)
		path := "/secrets/" + name + "/password"
		existing, err := os.ReadFile(path)
		if err == nil && subtle.ConstantTimeCompare(existing, []byte(password)) != 1 {
			return fmt.Errorf("%s secret differs from the existing Docker volume; restore the original .secrets directory (rotation is not automatic)", name)
		}
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		if os.IsNotExist(err) {
			if err = os.WriteFile(path, []byte(password), 0444); err != nil {
				return err
			}
		}
		if name == "grafana" {
			r.api.Password = password
		}
		if name == "exporter" {
			if err = os.WriteFile("/secrets/exporter/my.cnf", []byte("[client]\nuser=exporter\npassword="+password+"\n"), 0444); err != nil {
				return err
			}
		}
	}
	return nil
}

func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("symlinks are not accepted in config: %s", rel)
		}
		if d.IsDir() {
			return os.MkdirAll(target, 0755)
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, b, 0644)
	})
}

func (r *runner) redact(s string) string {
	for _, secret := range r.secrets {
		s = strings.ReplaceAll(s, secret, "[REDACTED]")
	}
	return s
}

func (r *runner) docker(ctx context.Context, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Dir = workdir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("docker %s: %w: %s", args[0], err, r.redact(string(out)))
	}
	return string(out), nil
}

func (r *runner) compose(ctx context.Context, layer string, args ...string) (string, error) {
	prefix := []string{"compose", "--project-name", "safeboard-" + layer, "--file", filepath.Join(workdir, "compose."+layer+".yaml")}
	return r.docker(ctx, append(prefix, args...)...)
}

func (r *runner) deployApp(ctx context.Context) error {
	fmt.Println("Deploy application: MariaDB -> Kuma")
	if _, err := r.compose(ctx, "app", "config", "--quiet"); err != nil {
		return err
	}
	if _, err := r.compose(ctx, "app", "up", "--detach", "--wait", "--wait-timeout", "180"); err != nil {
		return err
	}
	return r.waitHTTP(ctx, "Kuma", "http://kuma:3001/")
}

func (r *runner) deployMonitoring(ctx context.Context) error {
	fmt.Println("Deploy monitoring (application may be offline)")
	if _, err := r.compose(ctx, "monitoring", "config", "--quiet"); err != nil {
		return err
	}
	if _, err := r.docker(ctx, "run", "--rm", "--mount", "type=volume,src=safeboard-config,dst=/etc/safeboard,readonly", "--entrypoint", "promtool", "prom/prometheus:v3.7.3", "check", "config", "/etc/safeboard/prometheus/prometheus.yml"); err != nil {
		return err
	}
	if _, err := r.docker(ctx, "run", "--rm", "--mount", "type=volume,src=safeboard-config,dst=/etc/safeboard,readonly", "--workdir", "/etc/safeboard/prometheus", "--entrypoint", "promtool", "prom/prometheus:v3.7.3", "test", "rules", "rules.test.yml"); err != nil {
		return err
	}
	// Restart Prometheus after updating its mounted config; up alone does not reload it.
	if _, err := r.compose(ctx, "monitoring", "up", "--detach"); err != nil {
		return err
	}
	if _, err := r.compose(ctx, "monitoring", "restart", "prometheus", "blackbox-exporter"); err != nil {
		return err
	}
	for _, check := range [][2]string{{"Prometheus", "http://prometheus:9090/-/ready"}, {"Grafana", "http://grafana:3000/api/health"}, {"cAdvisor", "http://cadvisor:8080/metrics"}, {"node_exporter", "http://node-exporter:9100/metrics"}, {"mysqld_exporter", "http://mysqld-exporter:9104/metrics"}, {"blackbox_exporter", "http://blackbox-exporter:9115/metrics"}} {
		if err := r.waitHTTP(ctx, check[0], check[1]); err != nil {
			return err
		}
	}
	return nil
}

func (r *runner) waitHTTP(ctx context.Context, label, endpoint string) error {
	return Wait(ctx, readyTimeout, 2*time.Second, label, func(ctx context.Context) error {
		req, err := http.NewRequestWithContext(ctx, "GET", endpoint, nil)
		if err != nil {
			return err
		}
		res, err := r.api.Client.Do(req)
		if err != nil {
			return err
		}
		defer res.Body.Close()
		io.Copy(io.Discard, io.LimitReader(res.Body, 8<<20))
		if res.StatusCode != 200 {
			return fmt.Errorf("HTTP %d", res.StatusCode)
		}
		return nil
	})
}

func (r *runner) configure(ctx context.Context) error {
	fmt.Println("Configure Grafana through HTTP API")
	if err := r.waitHTTP(ctx, "Grafana", "http://grafana:3000/api/health"); err != nil {
		return err
	}
	raw, err := os.ReadFile(filepath.Join(workdir, "config/grafana/dashboard.json"))
	if err != nil {
		return err
	}
	var dashboard map[string]any
	if err = json.Unmarshal(raw, &dashboard); err != nil {
		return err
	}
	return r.api.Configure(ctx, dashboard)
}

func (r *runner) query(ctx context.Context, expr string) error {
	req, err := http.NewRequestWithContext(ctx, "GET", r.api.Prometheus+"/api/v1/query?query="+url.QueryEscape(expr), nil)
	if err != nil {
		return err
	}
	res, err := r.api.Client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return fmt.Errorf("Prometheus HTTP %d", res.StatusCode)
	}
	var value struct {
		Status string `json:"status"`
		Data   struct {
			Result []json.RawMessage `json:"result"`
		} `json:"data"`
	}
	if err = json.NewDecoder(io.LimitReader(res.Body, 4<<20)).Decode(&value); err != nil {
		return err
	}
	if value.Status != "success" || len(value.Data.Result) == 0 {
		return fmt.Errorf("missing metric: %s", expr)
	}
	return nil
}

func (r *runner) verify(ctx context.Context) error {
	fmt.Println("Validate containers, seven scrape targets, resources and Grafana health")
	for _, layer := range []string{"app", "monitoring"} {
		out, err := r.compose(ctx, layer, "ps", "--all", "--quiet")
		if err != nil {
			return err
		}
		ids := strings.Fields(out)
		expected := 2
		if layer == "monitoring" {
			expected = 6
		}
		if len(ids) != expected {
			return fmt.Errorf("%s: expected %d containers, got %d", layer, expected, len(ids))
		}
		for _, id := range ids {
			if err = r.waitContainer(ctx, id); err != nil {
				return err
			}
		}
	}
	// An HTTP setup page alone cannot prove that Kuma uses the external DB.
	kumaID, err := r.compose(ctx, "app", "ps", "--quiet", "kuma")
	if err != nil {
		return err
	}
	if _, err = r.docker(ctx, "exec", strings.TrimSpace(kumaID), "node", "-e", "const c=require('/app/data/db-config.json');if(c.type!=='mariadb')process.exit(1)"); err != nil {
		return fmt.Errorf("Kuma must use external MariaDB: %w", err)
	}
	if err := Wait(ctx, readyTimeout, 2*time.Second, "metrics", func(ctx context.Context) error {
		for _, expr := range []string{
			"count(up == 1) == 7",
			"node_memory_MemAvailable_bytes{job=\"node\"}",
			"container_memory_working_set_bytes{container_label_com_docker_compose_service=\"kuma\"}",
			"container_cpu_usage_seconds_total{container_label_com_docker_compose_service=\"mariadb\"}",
			"mysql_up{job=\"mysql\"} == 1",
		} {
			if err := r.query(ctx, expr); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return err
	}
	if err := r.waitGrafana(ctx, 1); err != nil {
		return err
	}
	var dashboard map[string]any
	_, err = r.api.request(ctx, "GET", "/api/dashboards/uid/safeboard-health", nil, &dashboard)
	return err
}

func (r *runner) waitContainer(ctx context.Context, id string) error {
	return Wait(ctx, readyTimeout, 2*time.Second, "container readiness", func(ctx context.Context) error {
		out, err := r.docker(ctx, "inspect", "--format", "{{.State.Running}} {{if .State.Health}}{{.State.Health.Status}}{{else}}healthy{{end}}", id)
		if err != nil {
			return err
		}
		if strings.TrimSpace(out) != "true healthy" {
			return fmt.Errorf("container state %s", strings.TrimSpace(out))
		}
		return nil
	})
}

func (r *runner) waitGrafana(ctx context.Context, want float64) error {
	return Wait(ctx, readyTimeout, 2*time.Second, "Grafana health", func(ctx context.Context) error {
		got, err := r.api.GrafanaHealth(ctx)
		if err != nil {
			return err
		}
		if got != want {
			return fmt.Errorf("health=%g, expected %g", got, want)
		}
		return nil
	})
}

func (r *runner) annotation(ctx context.Context, text string) error {
	_, err := r.api.request(ctx, "POST", "/api/annotations", map[string]any{"dashboardUID": "safeboard-health", "time": time.Now().UnixMilli(), "tags": []string{"chaos"}, "text": text}, nil)
	return err
}

func (r *runner) chaos(ctx context.Context, target, mode string) (err error) {
	if target != "kuma" && target != "mariadb" {
		return errors.New("target must be kuma or mariadb")
	}
	if mode != "both" && mode != "stop" && mode != "crash" {
		return errors.New("mode must be both, stop or crash")
	}
	if err = r.verify(ctx); err != nil {
		return err
	}
	out, err := r.compose(ctx, "app", "ps", "--quiet", target)
	if err != nil {
		return err
	}
	id := strings.TrimSpace(out)
	if id == "" || strings.ContainsAny(id, "\r\n ") {
		return errors.New("expected exactly one chaos target")
	}
	// Always recover even if the caller cancels, a query fails, or an assertion fails.
	defer func() {
		recoveryCtx, cancel := context.WithTimeout(context.Background(), readyTimeout+30*time.Second)
		defer cancel()
		_, startErr := r.docker(recoveryCtx, "start", id)
		if startErr == nil {
			startErr = r.waitContainer(recoveryCtx, id)
		}
		if startErr == nil {
			startErr = r.waitGrafana(recoveryCtx, 1)
		}
		if startErr != nil {
			err = errors.Join(err, fmt.Errorf("chaos cleanup: %w", startErr))
		}
	}()
	if mode == "both" || mode == "stop" {
		fmt.Printf("CHAOS %s: docker stop; waiting for red Grafana status\n", target)
		if _, err = r.docker(ctx, "stop", "--time", "20", id); err != nil {
			return err
		}
		if err = r.waitGrafana(ctx, 0); err != nil {
			return err
		}
		if err = r.annotation(ctx, target+": docker stop; Все плохо (verified via Grafana datasource API)"); err != nil {
			return err
		}
		// Hold the observed failure across several scrape/dashboard refresh intervals.
		timer := time.NewTimer(15 * time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
		if _, err = r.docker(ctx, "start", id); err != nil {
			return err
		}
		if err = r.waitContainer(ctx, id); err != nil {
			return err
		}
		if err = r.waitGrafana(ctx, 1); err != nil {
			return err
		}
		if err = r.annotation(ctx, target+": docker start; Все хорошо"); err != nil {
			return err
		}
		fmt.Println("PASS: observed red -> green through Grafana API")
	}
	if mode == "both" || mode == "crash" {
		// Docker only enables the restart policy after a successful ten-second start.
		if err = Wait(ctx, 30*time.Second, time.Second, "restart-policy activation", func(ctx context.Context) error {
			out, e := r.docker(ctx, "inspect", "--format", "{{.State.StartedAt}}", id)
			if e != nil {
				return e
			}
			started, e := time.Parse(time.RFC3339Nano, strings.TrimSpace(out))
			if e != nil {
				return e
			}
			if time.Since(started) < 11*time.Second {
				return errors.New("waiting for ten-second uptime")
			}
			return nil
		}); err != nil {
			return err
		}
		before, err := r.restartCount(ctx, id)
		if err != nil {
			return err
		}
		fmt.Printf("CHAOS %s: SIGKILL; restart count before=%d\n", target, before)
		// Docker 29 marks `docker kill` as a manual stop too. Signal the actual
		// init process from the daemon host PID namespace, without the kill API.
		pidText, pidErr := r.docker(ctx, "inspect", "--format", "{{.State.Pid}}", id)
		if pidErr != nil {
			return pidErr
		}
		pid, pidErr := strconv.Atoi(strings.TrimSpace(pidText))
		if pidErr != nil || pid <= 1 {
			return errors.New("refusing invalid container init PID")
		}
		if _, err = r.docker(ctx, "run", "--rm", "--pid=host", "--cap-drop=ALL", "--cap-add=KILL", "--user=0", "--entrypoint", "/bin/kill", "docker:28.5.1-cli", "-KILL", strconv.Itoa(pid)); err != nil {
			return err
		}
		if err = Wait(ctx, readyTimeout, 2*time.Second, "automatic restart", func(ctx context.Context) error {
			count, e := r.restartCount(ctx, id)
			if e != nil {
				return e
			}
			if count <= before {
				return errors.New("RestartCount did not increase")
			}
			return nil
		}); err != nil {
			return err
		}
		if err = r.waitContainer(ctx, id); err != nil {
			return err
		}
		if err = r.waitGrafana(ctx, 1); err != nil {
			return err
		}
		if err = r.annotation(ctx, target+": SIGKILL; restart-policy recovered service"); err != nil {
			return err
		}
		fmt.Println("PASS: automatic restart and healthy Grafana status")
	}
	return nil
}

func (r *runner) restartCount(ctx context.Context, id string) (int, error) {
	out, err := r.docker(ctx, "inspect", "--format", "{{.RestartCount}}", id)
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(strings.TrimSpace(out))
}
