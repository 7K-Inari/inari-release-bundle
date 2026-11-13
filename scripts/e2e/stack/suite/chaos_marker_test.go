package suite

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/onsi/ginkgo/v2/types"
)

func TestChaosGinkgoMarkerSkipsNonSpecsAndWritesOnce(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "nested", "ginkgo-marker.json")
	t.Setenv(ChaosGinkgoMarkerEnv, marker)
	t.Setenv("E2E_RUN_ID", "run-123")
	t.Setenv("CHAOS_SCENARIO", "keycloak-latency")
	chaosGinkgoMarker.written = false
	t.Cleanup(func() { chaosGinkgoMarker.written = false })

	for _, state := range []types.SpecState{types.SpecStateSkipped, types.SpecStatePending} {
		if err := MarkChaosGinkgoSpec(types.SpecReport{State: state, LeafNodeText: "not real"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("skipped/pending spec wrote marker: %v", err)
	}
	report := types.SpecReport{
		State:                   types.SpecStatePassed,
		LeafNodeText:            "reports the cluster active",
		ContainerHierarchyTexts: []string{"Cluster"},
	}
	if err := MarkChaosGinkgoSpec(report); err != nil {
		t.Fatal(err)
	}
	if err := MarkChaosGinkgoSpec(types.SpecReport{State: types.SpecStatePassed, LeafNodeText: "later spec"}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		Spec      string   `json:"spec"`
		Container []string `json:"container"`
		RunID     string   `json:"runId"`
		Scenario  string   `json:"scenario"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Spec != "reports the cluster active" || payload.RunID != "run-123" || payload.Scenario != "keycloak-latency" || len(payload.Container) != 1 {
		t.Fatalf("unexpected marker payload: %+v", payload)
	}
}

func TestChaosGinkgoMarkerUnsetIsInert(t *testing.T) {
	t.Setenv(ChaosGinkgoMarkerEnv, "")
	chaosGinkgoMarker.written = false
	if err := MarkChaosGinkgoSpec(types.SpecReport{State: types.SpecStatePassed}); err != nil {
		t.Fatal(err)
	}
}

func TestChaosMarkerDoesNotOverwrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "marker.json")
	if err := os.WriteFile(path, []byte("existing"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeChaosMarkerFile(path, map[string]string{"spec": "new"}); err == nil {
		t.Fatal("existing marker was overwritten")
	}
}
