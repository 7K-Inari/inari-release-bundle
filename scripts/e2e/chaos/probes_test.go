package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func timeoutTestAPI(t *testing.T, handler http.HandlerFunc) (*httptest.Server, *APIClient) {
	t.Helper()
	server := httptest.NewTLSServer(handler)
	api := &APIClient{
		BaseDomain: strings.TrimPrefix(server.URL, "https://"),
		KCURL:      server.URL,
		User:       "dev-admin",
		Password:   "secret",
		HTTP:       server.Client(),
	}
	return server, api
}

func TestOpenFGAProtectedReadClassification(t *testing.T) {
	cfg, err := loadConfig(getenvFor(validEnv()))
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name    string
		status  int
		wantErr bool
	}{
		{name: "forbidden is fail closed", status: http.StatusForbidden},
		{name: "server error is fail closed", status: http.StatusServiceUnavailable},
		{name: "2xx is never fail closed", status: http.StatusOK, wantErr: true},
		{name: "stale token is not evidence", status: http.StatusUnauthorized, wantErr: true},
		{name: "missing fresh object is not evidence", status: http.StatusNotFound, wantErr: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			server, api := timeoutTestAPI(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(`{"organization":{"id":"org-1","slug":"fresh-org"}}`))
			})
			defer server.Close()
			state := &DomainState{Tenant: &Tenant{ID: "org-1", Slug: "fresh-org"}, Token: "token"}
			err := duringEngine(context.Background(), cfg, api, nil, "openfga-timeout", state)
			if (err != nil) != tt.wantErr {
				t.Fatalf("duringEngine error=%v, wantErr=%t", err, tt.wantErr)
			}
			if state.TimeoutProbe == nil {
				t.Fatal("timeout outcome was not recorded")
			}
		})
	}
}

func TestOpenFGABoundedClientTimeoutIsFailClosed(t *testing.T) {
	server, api := timeoutTestAPI(t, func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(100 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	})
	defer server.Close()
	api.HTTP.Timeout = 10 * time.Millisecond
	cfg, err := loadConfig(getenvFor(validEnv()))
	if err != nil {
		t.Fatal(err)
	}
	state := &DomainState{Tenant: &Tenant{ID: "org-1", Slug: "fresh-org"}, Token: "token"}
	if err := duringEngine(context.Background(), cfg, api, nil, "openfga-timeout", state); err != nil {
		t.Fatalf("bounded timeout should pass: %v", err)
	}
	if state.TimeoutProbe == nil || !state.TimeoutProbe.TimedOut {
		t.Fatalf("timeout was not evidenced: %+v", state.TimeoutProbe)
	}
}

func TestNegativeControlRequiresAnOutageNot2xx(t *testing.T) {
	cfg, err := loadConfig(getenvFor(validEnv()))
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		status  int
		wantErr bool
	}{
		{status: http.StatusOK, wantErr: true},
		{status: http.StatusUnauthorized, wantErr: true},
		{status: http.StatusServiceUnavailable},
	} {
		server, api := timeoutTestAPI(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(tt.status)
		})
		err := duringEngine(context.Background(), cfg, api, nil, "negative-control", &DomainState{})
		server.Close()
		if (err != nil) != tt.wantErr {
			t.Fatalf("status %d: err=%v wantErr=%t", tt.status, err, tt.wantErr)
		}
	}
}

func TestOutboxRejectsUnsafeOrgID(t *testing.T) {
	if _, err := outboxCount(context.Background(), nil, "x' OR true --", true, false); err == nil {
		t.Fatal("unsafe SQL identifier accepted")
	}
}
