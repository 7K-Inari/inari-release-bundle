package suite

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/onsi/ginkgo/v2/types"
)

// ChaosGinkgoMarkerEnv is deliberately opt-in. Ordinary release, nightly,
// upgrade, and local runs leave it unset and pay no synchronization cost.
const ChaosGinkgoMarkerEnv = "E2E_CHAOS_GINKGO_MARKER_FILE"

var chaosGinkgoMarker = struct {
	sync.Mutex
	written bool
}{}

// MarkChaosGinkgoSpec writes once before the first real (not skipped/pending)
// spec. Ginkgo evaluates skip status before ReportBeforeEach, so label-filtered
// specs cannot trigger an early fault. The driver waits for this marker AND
// Playwright's first-test marker before applying a ChaosEngine.
func MarkChaosGinkgoSpec(report types.SpecReport) error {
	path := os.Getenv(ChaosGinkgoMarkerEnv)
	if path == "" {
		return nil
	}
	chaosGinkgoMarker.Lock()
	defer chaosGinkgoMarker.Unlock()
	if chaosGinkgoMarker.written || report.State.Is(types.SpecStateSkipped|types.SpecStatePending) {
		return nil
	}
	payload := map[string]any{
		"spec":      report.LeafNodeText,
		"container": report.ContainerHierarchyTexts,
		"runId":     os.Getenv("E2E_RUN_ID"),
		"scenario":  os.Getenv("CHAOS_SCENARIO"),
	}
	if err := writeChaosMarkerFile(path, payload); err != nil {
		return err
	}
	chaosGinkgoMarker.written = true
	return nil
}

func writeChaosMarkerFile(path string, payload any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return fmt.Errorf("create first-spec chaos marker: %w", err)
	}
	encodeErr := json.NewEncoder(file).Encode(payload)
	closeErr := file.Close()
	if encodeErr != nil {
		return fmt.Errorf("write first-spec chaos marker: %w", encodeErr)
	}
	return closeErr
}
