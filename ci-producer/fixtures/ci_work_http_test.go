//go:build darwin && cgo && ci_actual_producer

package residentpipe

import (
	"net/http"
	"sync"
	"testing"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentgateway"
)

// Observe only the HTTP method and private SID, without reading/replacing the
// request body or response. No token, payload or SID enters public diagnostics.
// Observation starts after the separate authentication-negative client is ready.
type ciWorkHTTP struct {
	mu sync.Mutex
	enabled, invalid bool
	sid string
	initializations, posts, deletes int
}
func (o *ciWorkHTTP) enable() { o.mu.Lock(); defer o.mu.Unlock(); o.enabled = true }
func (o *ciWorkHTTP) observe(method, sid string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if !o.enabled { return }
	if o.deletes != 0 { o.invalid = true }
	if sid == "" {
		if method != http.MethodPost { o.invalid = true }
		o.initializations++
		return
	}
	if o.sid == "" { o.sid = sid }
	if o.sid != sid || o.initializations != 1 { o.invalid = true }
	switch method {
	case http.MethodPost: o.posts++
	case http.MethodDelete: o.deletes++
	default: o.invalid = true
	}
}
func (o *ciWorkHTTP) confirmed(otherSID string) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.enabled && !o.invalid && o.initializations == 1 && o.posts > 0 && o.deletes == 1 && o.sid != "" && o.sid != otherSID
}
func (g *ciGateway) observeWorkHTTP(w http.ResponseWriter, r *http.Request) {
	g.workHTTP.observe(r.Method, r.Header.Get(agentgateway.HeaderSession))
	g.gateway.ServeHTTP(w, r)
}

// Pure sequence validation, not an HTTP/native execution or cleanup proof.
func TestCIWorkSessionObservation(t *testing.T) {
	for _, scenario := range []string{"same-session", "reinitialize", "changed-session", "after-delete", "double-delete", "no-delete", "other-client", "no-initialize"} {
		o := &ciWorkHTTP{}
		o.observe(http.MethodPost, "ignored-before-work")
		o.enable()
		if scenario != "no-initialize" { o.observe(http.MethodPost, "") }
		o.observe(http.MethodPost, "work-session")
		switch scenario {
		case "reinitialize": o.observe(http.MethodPost, "")
		case "changed-session": o.observe(http.MethodPost, "different-session")
		}
		if scenario != "no-delete" { o.observe(http.MethodDelete, "work-session") }
		switch scenario {
		case "after-delete": o.observe(http.MethodPost, "work-session")
		case "double-delete": o.observe(http.MethodDelete, "work-session")
		}
		other := "authentication-client"
		if scenario == "other-client" { other = "work-session" }
		if o.confirmed(other) != (scenario == "same-session") { t.Fatal("session observation mismatch", scenario) }
	}
}
