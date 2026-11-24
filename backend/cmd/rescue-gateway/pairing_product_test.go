//go:build !tether_development

package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentgateway"
)

func TestProductPairingRequiresConsent(t *testing.T) {
	// A production pairing configuration without browser consent must not start.
	doc := pairingDoc(t)
	delete(doc["pairing"].(map[string]any), "consent")
	if _, err := parseLocalConfig(encode(t, doc), configPath); err == nil {
		t.Fatal("product pairing without consent was accepted")
	}
}

func TestProductPairPathIsUnavailable(t *testing.T) {
	// The one-step registration path must be absent from a production gateway.
	legacy := legacyPairHandlerForBuild(nil, &pairingConfig{})
	root := routes("/mcp", "/register", false, legacy, http.NotFoundHandler(), http.NotFoundHandler(), nil)
	resp := httptest.NewRecorder()
	root.ServeHTTP(resp, httptest.NewRequest(http.MethodPost, "https://gateway.example.invalid"+agentgateway.PairPath, nil))
	if resp.Code != http.StatusNotFound {
		t.Fatalf("product legacy pair path status=%d, want 404", resp.Code)
	}
}
