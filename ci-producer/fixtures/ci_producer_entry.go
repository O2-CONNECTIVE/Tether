//go:build darwin && cgo && ci_actual_producer

package residentpipe

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agenttransport"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/macagent"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/residentmac"
	"golang.org/x/sys/unix"
	"io"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"
)

type ciBootstrap struct {
	Policy   string `json:"policy"`
	Endpoint string `json:"endpoint"`
	CA       []byte `json:"ca"`
}

var ciTrust *agenttransport.CIProducerTrust
var ciEntered atomic.Bool

// CIProducerMain is only linked into the separately compiled fixture helper.
// FD3 is bootstrap only: close it before original capturePipes/run begins.
func CIProducerMain() int {
	return ciProducerMain(jsonInput)
}

// CIForegroundMain is selected only by the reviewed CLI composition overlay.
// The ordinary CLI main/argument handling is not replaced.
func CIForegroundMain() int {
	return ciProducerMain(foregroundInput)
}

func ciProducerMain(dialect inputDialect) int {
	if !ciEntered.CompareAndSwap(false, true) {
		return 1
	}
	// Diagnostic FD is inherited only by this helper, never by native work.
	if unix.SetNonblock(4, true) != nil {
		return 1
	}
	if _, err := unix.FcntlInt(4, unix.F_SETFD, unix.FD_CLOEXEC); err != nil {
		return 1
	}
	if flags, err := unix.FcntlInt(4, unix.F_GETFD, 0); err != nil || flags&unix.FD_CLOEXEC == 0 {
		return 1
	}
	var nativeStat unix.Stat_t
	if unix.Fstat(4, &nativeStat) != nil || nativeStat.Mode&unix.S_IFMT != unix.S_IFIFO {
		return 1
	}
	// Configure only our inherited bootstrap pipe for bounded Go polling.
	if unix.SetNonblock(3, true) != nil {
		return 1
	}
	f := os.NewFile(3, "owned-ci-bootstrap")
	if f == nil {
		return 1
	}
	st, err := f.Stat()
	if err != nil || st.Mode()&os.ModeNamedPipe == 0 {
		_ = f.Close()
		return 1
	}
	if err = f.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		_ = f.Close()
		return 1
	}
	raw, err := io.ReadAll(io.LimitReader(f, 16385))
	closed := f.Close()
	if err != nil || closed != nil || len(raw) > 16384 {
		clear(raw)
		return 1
	}
	defer clear(raw)
	var b ciBootstrap
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&b) != nil || d.Decode(new(any)) != io.EOF || !filepath.IsAbs(b.Policy) || filepath.Clean(b.Policy) != b.Policy {
		return 1
	}
	trust, err := agenttransport.NewCIProducerTrust(b.Endpoint, b.CA)
	clear(b.CA)
	if err != nil {
		return 1
	}
	ciTrust = trust
	if !macagent.CIEnableNativeDiagnosticSink() {
		return 1
	}
	var result error
	if dialect == foregroundInput {
		result = runForeground(b.Policy)
	} else {
		result = run(b.Policy)
	}
	code := ExitCode(result)
	macagent.CIDisableNativeDiagnosticSink()
	if unix.Close(4) != nil {
		return 1
	}
	return code
}
func ciLoadStandby(ctx context.Context, policy string) (*residentmac.Standby, error) {
	return residentmac.CILoadProducer(ctx, policy, ciTrust)
}
