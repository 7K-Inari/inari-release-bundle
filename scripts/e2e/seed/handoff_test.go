// Tests for the handoff publish path: the ConfigMap key-set contract with
// stack/suite/env.go, the SA REST create/patch behavior, and the toolbox
// pod shape.
package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
)

func TestHandoffKeysMatchSuiteReader(t *testing.T) {
	// stack/suite/env.go loadHandoffConfigMap requires every one of these
	// non-empty (kind-era git_host_dir/agent_chart_dir are optional there).
	// agent_namespace is OPTIONAL on the reader side (defaulted to
	// inari-system for kind file handoffs and stale ConfigMaps) but the seed
	// always publishes it — pin it here so the contract never silently drops.
	want := []string{"namespace", "toolbox", "tenant", "kc_uid", "org_kc_id", "cluster_id", "org_id", "agent_namespace"}
	if !slices.Equal(handoffKeys, want) {
		t.Fatalf("handoffKeys = %v, want %v", handoffKeys, want)
	}
}

func TestSAStoreWriteConfigMap(t *testing.T) {
	var created bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET":
			http.NotFound(w, r)
		case r.Method == "POST":
			created = true
			writeJSON(w, 201, map[string]any{"kind": "ConfigMap"})
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	s := &saStore{base: srv.URL, token: "t", http: srv.Client()}
	data := map[string]string{"tenant": "e2e-org"}
	if err := s.WriteConfigMap("inari-e2e", "e2e-handoff", data); err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Fatal("missing CM: no create issued")
	}

	// Existing ConfigMap → PUT (update verb) with resourceVersion + merged data.
	var putBody struct {
		Metadata map[string]any    `json:"metadata"`
		Data     map[string]string `json:"data"`
	}
	var put bool
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			writeJSON(w, 200, map[string]any{
				"kind":     "ConfigMap",
				"metadata": map[string]any{"name": "e2e-handoff", "resourceVersion": "42"},
				"data":     map[string]string{"foreign": "keep"},
			})
			return
		}
		if r.Method == "PUT" {
			put = true
			_ = json.NewDecoder(r.Body).Decode(&putBody)
			writeJSON(w, 200, map[string]any{"kind": "ConfigMap"})
			return
		}
		t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
	}))
	defer srv2.Close()

	s2 := &saStore{base: srv2.URL, token: "t", http: srv2.Client()}
	if err := s2.WriteConfigMap("inari-e2e", "e2e-handoff", data); err != nil {
		t.Fatal(err)
	}
	if !put {
		t.Fatal("existing CM: no PUT issued")
	}
	if putBody.Metadata["resourceVersion"] != "42" {
		t.Fatalf("PUT metadata = %v — resourceVersion required for update", putBody.Metadata)
	}
	if putBody.Data["tenant"] != "e2e-org" || putBody.Data["foreign"] != "keep" {
		t.Fatalf("PUT data = %v — want merged (new keys set, foreign keys kept)", putBody.Data)
	}
}

func TestToolboxPodShape(t *testing.T) {
	pod := toolboxPod("inari-e2e", "e2e-toolbox", "curlimages/curl:8.10.1")
	spec, _ := pod["spec"].(map[string]any)
	containers, _ := spec["containers"].([]map[string]any)
	if len(containers) != 1 {
		t.Fatalf("containers = %v", containers)
	}
	cmd, _ := containers[0]["command"].([]string)
	if !slices.Equal(cmd, []string{"sleep", "infinity"}) {
		t.Fatalf("command = %v — must be sleep infinity (suite legs may start long after seeding)", cmd)
	}
	sc, _ := spec["securityContext"].(map[string]any)
	if sc["runAsNonRoot"] != true {
		t.Fatalf("securityContext = %v — PSA-restricted namespaces need runAsNonRoot", sc)
	}
}
