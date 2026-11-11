package main

import (
	"context"
	"io"
	"net/http"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentgateway"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentkeys"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentwire"
)

// Fixed from entry, before any document read. No flag, environment variable,
// document member, request or session can extend the discovery admission window.
const discoveryMaxLifetime = 5 * time.Minute

func parseDiscoveryConfig(raw []byte) (agentgateway.FileConfig, error) {
	if len(raw) == 0 || len(raw) > agentkeys.MaxTrustConfigBytes || agentwire.CheckJSON(raw, false) != nil {
		return agentgateway.FileConfig{}, errConfig
	}
	fields, ok := members(raw, "mode", "gateway")
	if !ok {
		return agentgateway.FileConfig{}, errConfig
	}
	defer wipeMembers(fields)
	mode, ok := text(fields["mode"], 32)
	if !ok || mode != "discovery_only" {
		return agentgateway.FileConfig{}, errConfig
	}
	return agentgateway.ParseConfig(fields["gateway"])
}

// No signer read, registration handler, resident enrollment, grant or command
// producer exists in this composition. Existing auth and membership remain.
func executeDiscovery(parent context.Context, path string, diagnostic io.Writer, d deps) (code int) {
	if parent == nil || parent.Err() != nil || path == "" || d.readTrust == nil || d.listen == nil {
		report(diagnostic, "Discovery mode is not configured")
		return 2
	}
	ctx, stop := context.WithTimeout(parent, discoveryMaxLifetime)
	defer stop()
	read, cancel := context.WithTimeout(ctx, readBudget)
	raw, err := d.readTrust(read, path)
	cancel()
	if err != nil {
		wipe(raw)
		report(diagnostic, "Discovery configuration unavailable")
		return 2
	}
	cfg, err := parseDiscoveryConfig(raw)
	wipe(raw)
	if err != nil {
		report(diagnostic, "Invalid discovery configuration")
		return 2
	}
	gwConfig, err := cfg.GatewayConfig()
	if err != nil {
		report(diagnostic, "Invalid discovery configuration")
		return 2
	}
	gwConfig.DiscoveryOnly = true
	gw, err := agentgateway.New(gwConfig)
	if err != nil {
		report(diagnostic, "Invalid discovery configuration")
		return 2
	}
	stopRefresh, err := startAccessRefresh(ctx, cfg, gwConfig.Access, diagnostic, d)
	if err != nil {
		report(diagnostic, "Access公開鍵を取得できませんでした。待受しません")
		return closeUnserved(nil, gw.Close, diagnostic, 2)
	}
	defer func() {
		if err := stopRefresh(); err != nil {
			report(diagnostic, "Owned shutdown could not be confirmed")
			code = 1
		}
	}()
	if ctx.Err() != nil {
		return closeUnserved(nil, gw.Close, diagnostic, 1)
	}
	ln, err := d.listen(cfg.ListenNetwork(), cfg.Listen)
	if err != nil || ln == nil {
		if ln != nil {
			_ = ln.Close()
		}
		report(diagnostic, "Discovery listener unavailable")
		return closeUnserved(nil, gw.Close, diagnostic, 1)
	}
	handler := discoveryRoutes(ctx, pathOf(cfg.Resource), cfg.Mode == agentgateway.ModeOAuthBearer, gw)
	code = serve(ctx, ln, handler, func(context.Context) error { return nil }, gw.Close, diagnostic)
	if ctx.Err() == context.DeadlineExceeded {
		report(diagnostic, "Discovery pilot expired")
		return 1
	}
	return code
}

// Exact routes only, including direct backend access. The deadline also closes
// admission during owner cleanup. Metadata remains specific to the auth mode.
func discoveryRoutes(ctx context.Context, mcpPath string, metadata bool, gw http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL == nil || r.URL.RawPath != "" || r.URL.RawQuery != "" || r.URL.ForceQuery {
			http.NotFound(w, r)
			return
		}
		p := r.URL.Path
		if p != mcpPath && !(metadata && (p == agentgateway.MetadataPrefix || p == agentgateway.MetadataPrefix+mcpPath)) {
			http.NotFound(w, r)
			return
		}
		if ctx.Err() != nil {
			http.Error(w, "Discovery pilot ended", http.StatusServiceUnavailable)
			return
		}
		gw.ServeHTTP(w, r)
	})
}
