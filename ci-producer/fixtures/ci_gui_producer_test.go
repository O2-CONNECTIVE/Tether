//go:build darwin && cgo && ci_actual_producer

package residentpipe

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agenttransport"
)

// A dedicated Go test executable is the GUI's exact owned pipe producer.
// Only the compiler overlay supplies synthetic loopback trust. Normal startup
// remains unavailable. No work request or native command is issued here.
func TestCIResidentGUIProducer(t *testing.T) {
	if os.Getenv("CI_ACTUAL_PRODUCER") != "1" || os.Getenv("RUNNER_ENVIRONMENT") != "github-hosted" || os.Getenv("RUNNER_OS") != "macOS" || os.Getenv("RUNNER_ARCH") != "ARM64" {
		t.Fatal("requires explicit disposable Mac ARM64 lane")
	}
	// The testing runner must never write its own PASS line into protocol stdout.
	// capturePipes below owns fd 1 independently of this Go variable.
	os.Stdout = os.Stderr
	var gateway *ciGateway
	confirmed := false
	t.Cleanup(func() {
		if confirmed {
			return
		}
		// No retry, signal or success exit after uncertain ownership.
		for {
			time.Sleep(time.Hour)
			runtime.KeepAlive(gateway)
		}
	})
	// Match the existing actual-producer fixture: policy and key must be below
	// the canonical checkout, not the default world-writable /tmp ancestor.
	// The unchanged protected reader still validates every held path component.
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal("fixture working directory unavailable")
	}
	cwd, err = filepath.EvalSymlinks(cwd)
	if err != nil {
		t.Fatal("fixture working directory unresolved")
	}
	root, err := os.MkdirTemp(cwd, ".ci-gui-producer-")
	if err != nil {
		t.Fatal("fixture root unavailable")
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal("fixture root unresolved")
	}
	diag := &ciDiagnostic{stage: "gateway-setup"}
	gateway = newCIGateway(t, root, diag)
	ciTrust, err = agenttransport.NewCIProducerTrust(gateway.endpoint, gateway.caDER)
	if err != nil {
		t.Fatal("fixture trust unavailable")
	}
	// This is the existing production JSONL composition and real Standby owner,
	// with only its source-pinned loader expression replaced by the overlay.
	if err = run(gateway.policyPath); err != nil {
		t.Fatal("producer did not close")
	}
	// run has returned only after the original Standby Close is confirmed.
	// The original process cannot exit until every gateway owner is joined too.
	if err = gateway.close(); err != nil {
		t.Fatal("gateway did not close")
	}
	if err = os.RemoveAll(root); err != nil {
		t.Fatal("fixture disposal unknown")
	}
	confirmed = true
}
