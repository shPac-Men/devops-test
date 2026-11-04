package platform

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestGrafanaUpsert(t *testing.T) {
	for _, existing := range []bool{false, true} {
		t.Run(fmt.Sprint(existing), func(t *testing.T) {
			var methods []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				user, password, ok := r.BasicAuth()
				if !ok || user != "admin" || password != "secret" {
					t.Error("missing authentication")
				}
				methods = append(methods, r.Method+" "+r.URL.Path)
				if r.Method == "GET" && !existing {
					w.WriteHeader(404)
					return
				}
				if r.Method == "POST" && r.URL.Path == "/api/dashboards/db" {
					var body struct {
						Overwrite bool
						Dashboard map[string]any
					}
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Fatal(err)
					}
					if !body.Overwrite || body.Dashboard["uid"] != "safeboard-health" {
						t.Error("dashboard must have stable UID and overwrite")
					}
				}
				w.Write([]byte(`{}`))
			}))
			defer server.Close()
			api := &API{Client: server.Client(), Grafana: server.URL, Prometheus: "http://prometheus:9090", Password: "secret"}
			if err := api.Configure(context.Background(), map[string]any{"uid": "safeboard-health"}); err != nil {
				t.Fatal(err)
			}
			action := "POST /api/datasources"
			if existing {
				action = "PUT /api/datasources/uid/safeboard-prometheus"
			}
			want := []string{"GET /api/datasources/uid/safeboard-prometheus", action, "POST /api/dashboards/db"}
			if !reflect.DeepEqual(methods, want) {
				t.Fatalf("got %v, want %v", methods, want)
			}
		})
	}
}

func TestGrafanaUnauthorizedIsNotTreatedAsMissing(t *testing.T) {
	count := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count++
		w.WriteHeader(401)
		w.Write([]byte("secret password response"))
	}))
	defer server.Close()
	api := &API{Client: server.Client(), Grafana: server.URL}
	err := api.Configure(context.Background(), nil)
	if err == nil || !strings.Contains(err.Error(), "401") || strings.Contains(err.Error(), "secret password") {
		t.Fatalf("unsafe or missing error: %v", err)
	}
	if count != 1 {
		t.Fatal("unauthorized request must not create anything")
	}
}

func TestHealthRejectsMissingStaleAndInvalidSamples(t *testing.T) {
	for _, tc := range []struct {
		name   string
		sample string
		valid  bool
	}{
		{"healthy", fmt.Sprintf(`[[%d,"1"]]`, time.Now().Unix()), true},
		{"down", fmt.Sprintf(`[[%d,"0"]]`, time.Now().Unix()), true},
		{"stale", `[[1,"1"]]`, false}, {"missing", `[]`, false},
		{"nan", fmt.Sprintf(`[[%d,"NaN"]]`, time.Now().Unix()), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var samples [][]any
			if err := json.Unmarshal([]byte(tc.sample), &samples); err != nil {
				t.Fatal(err)
			}
			var result []map[string]any
			for _, s := range samples {
				result = append(result, map[string]any{"value": s})
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				json.NewEncoder(w).Encode(map[string]any{"status": "success", "data": map[string]any{"result": result}})
			}))
			defer server.Close()
			api := &API{Client: server.Client(), Grafana: server.URL}
			_, err := api.GrafanaHealth(context.Background())
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v, err=%v", tc.valid, err)
			}
		})
	}
}

func TestWaitHonorsDeadlineAndCancellation(t *testing.T) {
	start := time.Now()
	err := Wait(context.Background(), 20*time.Millisecond, time.Second, "test", func(context.Context) error { return errors.New("not ready") })
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > 300*time.Millisecond {
		t.Fatalf("deadline was not enforced: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	calls := 0
	err = Wait(ctx, time.Second, time.Second, "cancelled", func(context.Context) error { calls++; return nil })
	if !errors.Is(err, context.Canceled) || calls != 0 {
		t.Fatalf("cancelled check ran: %v", err)
	}
}

func TestWaitRetriesUntilReady(t *testing.T) {
	count := 0
	err := Wait(context.Background(), time.Second, time.Millisecond, "test", func(context.Context) error {
		count++
		if count < 3 {
			return errors.New("not yet")
		}
		return nil
	})
	if err != nil || count != 3 {
		t.Fatalf("count=%d err=%v", count, err)
	}
}
