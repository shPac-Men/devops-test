package platform

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

type API struct {
	Client     *http.Client
	Grafana    string
	Prometheus string
	Password   string
}

func (a *API) request(ctx context.Context, method, path string, payload any, out any) (int, error) {
	var b bytes.Buffer
	if payload != nil {
		if err := json.NewEncoder(&b).Encode(payload); err != nil {
			return 0, err
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, a.Grafana+path, &b)
	if err != nil {
		return 0, err
	}
	req.SetBasicAuth("admin", a.Password)
	req.Header.Set("Content-Type", "application/json")
	res, err := a.Client.Do(req)
	if err != nil {
		return 0, err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		// Responses may contain credentials echoed by downstream services.
		return res.StatusCode, fmt.Errorf("Grafana %s %s: HTTP %d", method, path, res.StatusCode)
	}
	if out != nil {
		err = json.NewDecoder(io.LimitReader(res.Body, 4<<20)).Decode(out)
	}
	return res.StatusCode, err
}

// Configure uses stable UIDs and checks for existence before creating resources.
func (a *API) Configure(ctx context.Context, dashboard map[string]any) error {
	datasource := map[string]any{"uid": "safeboard-prometheus", "name": "Safeboard Prometheus", "type": "prometheus", "access": "proxy", "url": a.Prometheus, "isDefault": true, "jsonData": map[string]any{"httpMethod": "POST", "timeInterval": "5s"}}
	code, err := a.request(ctx, "GET", "/api/datasources/uid/safeboard-prometheus", nil, nil)
	if code == 404 {
		_, err = a.request(ctx, "POST", "/api/datasources", datasource, nil)
	} else if err == nil {
		_, err = a.request(ctx, "PUT", "/api/datasources/uid/safeboard-prometheus", datasource, nil)
	}
	if err != nil {
		return err
	}
	_, err = a.request(ctx, "POST", "/api/dashboards/db", map[string]any{"dashboard": dashboard, "overwrite": true, "message": "Managed by Dagger"}, nil)
	return err
}

// GrafanaHealth evaluates the dashboard's actual data source via Grafana API.
func (a *API) GrafanaHealth(ctx context.Context) (float64, error) {
	var result struct {
		Status string `json:"status"`
		Data   struct {
			Result []struct {
				Value []json.RawMessage `json:"value"`
			} `json:"result"`
		} `json:"data"`
	}
	_, err := a.request(ctx, "GET", "/api/datasources/proxy/uid/safeboard-prometheus/api/v1/query?query="+url.QueryEscape("safeboard:platform_ok"), nil, &result)
	if err != nil {
		return 0, err
	}
	if result.Status != "success" || len(result.Data.Result) != 1 || len(result.Data.Result[0].Value) != 2 {
		return 0, errors.New("Grafana health query returned no unique sample")
	}
	var timestamp float64
	var value string
	if err = json.Unmarshal(result.Data.Result[0].Value[0], &timestamp); err != nil {
		return 0, err
	}
	if time.Since(time.Unix(int64(timestamp), 0)) > 20*time.Second {
		return 0, errors.New("Grafana health sample is stale")
	}
	if err = json.Unmarshal(result.Data.Result[0].Value[1], &value); err != nil {
		return 0, err
	}
	number, err := strconv.ParseFloat(value, 64)
	if err != nil || (number != 0 && number != 1) {
		return 0, errors.New("invalid health value")
	}
	return number, nil
}

// Wait never outlives the caller's deadline and retains the last useful failure.
func Wait(ctx context.Context, timeout, interval time.Duration, label string, check func(context.Context) error) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var last error
	for {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("%s: %w (last check: %v)", label, err, last)
		}
		if last = check(ctx); last == nil {
			return nil
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return fmt.Errorf("%s: %w (last check: %v)", label, ctx.Err(), last)
		case <-timer.C:
		}
	}
}
