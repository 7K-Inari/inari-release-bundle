// httptest-driven tests for the KC and inari-server REST clients: wire
// shapes, error surfaces, and the idempotency rules (409 = joined, 404 =
// unset/projection-lag).
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// mux is a tiny route table for the fake KC/API servers.
type mux struct {
	t      *testing.T
	routes map[string]http.HandlerFunc
}

func newMux(t *testing.T) *mux { return &mux{t: t, routes: map[string]http.HandlerFunc{}} }

func (m *mux) handle(method, path string, fn http.HandlerFunc) {
	m.routes[method+" "+path] = fn
}

func (m *mux) serveHTTP(w http.ResponseWriter, r *http.Request) {
	if h, ok := m.routes[r.Method+" "+r.URL.Path]; ok {
		h(w, r)
		return
	}
	m.t.Errorf("unexpected %s %s", r.Method, r.URL.String())
	http.NotFound(w, r)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if v != nil {
		_ = json.NewEncoder(w).Encode(v)
	}
}

func TestKCToken(t *testing.T) {
	m := newMux(t)
	m.handle("POST", "/realms/inari/protocol/openid-connect/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.Form.Get("grant_type") != "client_credentials" {
			t.Errorf("grant_type = %q", r.Form.Get("grant_type"))
		}
		writeJSON(w, 200, map[string]string{"access_token": "tok123"})
	})
	srv := httptest.NewServer(http.HandlerFunc(m.serveHTTP))
	defer srv.Close()

	tok, err := NewKC(srv.URL, "inari").ClientCredentialsToken("inari-platform-admin", "s3cret")
	if err != nil {
		t.Fatal(err)
	}
	if tok != "tok123" {
		t.Fatalf("token = %q", tok)
	}
}

func TestKCTokenErrorCarriesStatus(t *testing.T) {
	m := newMux(t)
	m.handle("POST", "/realms/inari/protocol/openid-connect/token", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 401, map[string]string{"error": "invalid_client"})
	})
	srv := httptest.NewServer(http.HandlerFunc(m.serveHTTP))
	defer srv.Close()

	_, err := NewKC(srv.URL, "inari").UserToken("dev-admin", "wrong")
	if err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("err = %v, want a 401 surface", err)
	}
}

func TestKCCreateUserReadsLocationID(t *testing.T) {
	m := newMux(t)
	m.handle("POST", "/admin/realms/inari/users", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "http://kc/admin/realms/inari/users/u-42")
		w.WriteHeader(201)
	})
	srv := httptest.NewServer(http.HandlerFunc(m.serveHTTP))
	defer srv.Close()

	id, err := NewKC(srv.URL, "inari").CreateUser("at", map[string]any{"username": "e2e-viewer"})
	if err != nil {
		t.Fatal(err)
	}
	if id != "u-42" {
		t.Fatalf("id = %q", id)
	}
}

func TestKCJoinOrganization409IsSuccess(t *testing.T) {
	m := newMux(t)
	var gotBody string
	m.handle("POST", "/admin/realms/inari/organizations/org-1/members", func(w http.ResponseWriter, r *http.Request) {
		var v string
		_ = json.NewDecoder(r.Body).Decode(&v)
		gotBody = v
		writeJSON(w, 409, map[string]string{"errorMessage": "already a member"})
	})
	srv := httptest.NewServer(http.HandlerFunc(m.serveHTTP))
	defer srv.Close()

	if err := NewKC(srv.URL, "inari").JoinOrganization("at", "org-1", "u-1"); err != nil {
		t.Fatalf("409 must be treated as already-joined: %v", err)
	}
	if gotBody != "u-1" {
		t.Fatalf("member body = %q, want the bare JSON string user id", gotBody)
	}
}

func TestKCJoinOrganizationPropagatesRealErrors(t *testing.T) {
	m := newMux(t)
	m.handle("POST", "/admin/realms/inari/organizations/org-1/members", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 500, map[string]string{"errorMessage": "boom"})
	})
	srv := httptest.NewServer(http.HandlerFunc(m.serveHTTP))
	defer srv.Close()

	if err := NewKC(srv.URL, "inari").JoinOrganization("at", "org-1", "u-1"); err == nil {
		t.Fatal("500 must propagate")
	}
}

func TestAPIListTenantsAndCreate(t *testing.T) {
	m := newMux(t)
	m.handle("GET", "/tenants", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"tenants": []map[string]any{
			{"id": "o-1", "slug": "e2e-org", "displayName": "E2E Org", "keycloakOrgId": "kc-org-1"},
		}})
	})
	m.handle("POST", "/tenants", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["slug"] != "e2e-org" || body["displayName"] != "E2E Org" {
			t.Errorf("create body = %v", body)
		}
		writeJSON(w, 201, map[string]any{
			"organization": map[string]any{"id": "o-1", "slug": "e2e-org", "keycloakOrgId": "kc-org-1"},
		})
	})
	srv := httptest.NewServer(http.HandlerFunc(m.serveHTTP))
	defer srv.Close()

	api := NewAPI(srv.URL)
	api.SetToken("tok")
	tenants, err := api.ListTenants()
	if err != nil {
		t.Fatal(err)
	}
	if len(tenants) != 1 || tenants[0].KeycloakOrgID != "kc-org-1" {
		t.Fatalf("tenants = %+v", tenants)
	}
	kcID, err := api.CreateTenant("e2e-org", "E2E Org")
	if err != nil {
		t.Fatal(err)
	}
	if kcID != "kc-org-1" {
		t.Fatalf("keycloakOrgId = %q", kcID)
	}
}

func TestAPIAssignRole404IsProjectionLag(t *testing.T) {
	m := newMux(t)
	m.handle("PUT", "/tenants/e2e-org/members/u-1", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 404, map[string]string{"title": "user not found"})
	})
	srv := httptest.NewServer(http.HandlerFunc(m.serveHTTP))
	defer srv.Close()

	api := NewAPI(srv.URL)
	api.SetToken("tok")
	err := api.AssignRole("e2e-org", "u-1", "r-viewer")
	var se *StatusError
	if !errors.As(err, &se) || se.Status != 404 {
		t.Fatalf("err = %v, want StatusError 404 (projection-lag retry signal)", err)
	}
}

func TestAPIGetGitConfig404MeansUnset(t *testing.T) {
	m := newMux(t)
	m.handle("GET", "/tenants/e2e-org/git-config", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 404, map[string]string{"title": "git config not set"})
	})
	srv := httptest.NewServer(http.HandlerFunc(m.serveHTTP))
	defer srv.Close()

	api := NewAPI(srv.URL)
	api.SetToken("tok")
	cfg, err := api.GetGitConfig("e2e-org")
	if err != nil {
		t.Fatal(err)
	}
	if cfg != nil {
		t.Fatalf("cfg = %+v, want nil (unset)", cfg)
	}
}

func TestAPIGetGitConfigReadsConfig(t *testing.T) {
	m := newMux(t)
	m.handle("GET", "/tenants/e2e-org/git-config", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"config": map[string]string{
			"repo": "e2e-org/e2e-org-inari-state", "commitPolicy": "direct",
		}})
	})
	srv := httptest.NewServer(http.HandlerFunc(m.serveHTTP))
	defer srv.Close()

	api := NewAPI(srv.URL)
	api.SetToken("tok")
	cfg, err := api.GetGitConfig("e2e-org")
	if err != nil {
		t.Fatal(err)
	}
	if cfg == nil || cfg.Repo != "e2e-org/e2e-org-inari-state" {
		t.Fatalf("cfg = %+v", cfg)
	}
}

func ExampleStatusError_Error() {
	fmt.Println((&StatusError{Status: 409, Body: `{"title":"conflict"}`}).Error())
	// Output: 409: {"title":"conflict"}
}

func TestKCGroupByPath(t *testing.T) {
	m := newMux(t)
	m.handle("GET", "/admin/realms/inari/group-by-path/tenant-e2e-org/viewers", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]string{"id": "grp-42"})
	})
	srv := httptest.NewServer(http.HandlerFunc(m.serveHTTP))
	defer srv.Close()

	id, err := NewKC(srv.URL, "inari").GroupByPath("tok", "tenant-e2e-org/viewers")
	if err != nil {
		t.Fatal(err)
	}
	if id != "grp-42" {
		t.Fatalf("id = %q", id)
	}
}

func TestKCGroupByPathAbsent(t *testing.T) {
	// A missing group 404s — that is "", nil (org-sync lag polls on it).
	m := newMux(t)
	m.handle("GET", "/admin/realms/inari/group-by-path/tenant-nope/viewers", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 404, map[string]string{"error": "Group not found"})
	})
	srv := httptest.NewServer(http.HandlerFunc(m.serveHTTP))
	defer srv.Close()

	id, err := NewKC(srv.URL, "inari").GroupByPath("tok", "tenant-nope/viewers")
	if err != nil {
		t.Fatal(err)
	}
	if id != "" {
		t.Fatalf("id = %q, want empty for a missing group", id)
	}
}
