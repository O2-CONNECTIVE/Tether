//go:build darwin && cgo && ci_actual_producer

package residentpipe

import "testing"

// Fixed public enums only. Never include errors, paths, payloads or identities.
// The stage is set before an operation; failure records the first failing site.
type ciDiagnostic struct{ stage, failure string }

func (d *ciDiagnostic) at(stage string) { d.stage = stage }
func (d *ciDiagnostic) note(code string) {
	if d.failure == "" {
		d.failure = code
	}
}
func (d *ciDiagnostic) fail(t *testing.T, code string) {
	t.Helper()
	d.note(code)
	t.Fatal("fixture assertion failed; see fixed outcome codes")
}
func (d *ciDiagnostic) fields() (string, string) {
	failure := d.failure
	if failure == "" {
		failure = "uncategorized"
	}
	return d.stage, failure
}

func ciChildFailure(p *ciChild, err error) string {
	if p != nil {
		p.mu.Lock()
		code := p.diagnosticCode
		if code == "" && p.waitErr != nil {
			code = "producer-wait-nonzero"
		}
		p.mu.Unlock()
		if code != "" {
			return code
		}
	}
	if err == nil {
		return "uncategorized"
	}
	switch err.Error() {
	case "no fresh producer snapshot":
		return "producer-stale-snapshot"
	case "producer ended before condition":
		return "producer-ended"
	case "producer observation deadline; original owner retained":
		return "producer-observation-deadline"
	case "applied transition missing accepted same-ID receipt":
		return "producer-receipt-mismatch"
	case "original producer EOF not joined":
		return "producer-eof-deadline"
	case "original child Wait not joined":
		return "producer-wait-deadline"
	}
	return "producer-operation"
}
