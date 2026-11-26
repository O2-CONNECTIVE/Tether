package main

import (
	"net/http"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentgateway"
)

func legacyPairHandlerForBuild(gw *agentgateway.Gateway, cfg *pairingConfig) http.Handler {
	if !legacyPairBuildEnabled() || gw == nil || cfg == nil {
		return nil
	}
	return http.HandlerFunc(gw.ServePair)
}

func pairingRoutes(gw *agentgateway.Gateway, cfg *pairingConfig) http.Handler {
	legacy := legacyPairHandlerForBuild(gw, cfg)
	if cfg == nil || (cfg.Consent == nil && legacy == nil) {
		return nil
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case agentgateway.PairPath:
			if legacy != nil {
				legacy.ServeHTTP(w, r)
			} else {
				http.NotFound(w, r)
			}
		case agentgateway.PairOperationsPath:
			if cfg.Consent == nil {
				http.NotFound(w, r)
				return
			}
			gw.ServePairOperations(w, r)
		case agentgateway.PairConsentPath:
			if cfg.Consent == nil {
				http.NotFound(w, r)
				return
			}
			gw.ServePairConsent(w, r)
		case agentgateway.PairConsentCallbackPath:
			if cfg.Consent == nil {
				http.NotFound(w, r)
				return
			}
			gw.ServePairConsentCallback(w, r)
		case agentgateway.PairConfirmPath:
			if cfg.Consent == nil {
				http.NotFound(w, r)
				return
			}
			gw.ServePairConfirm(w, r)
		default:
			http.NotFound(w, r)
		}
	})
}

func isPairEndpointPath(path string) bool {
	switch path {
	case agentgateway.PairPath, agentgateway.PairOperationsPath, agentgateway.PairConsentPath, agentgateway.PairConsentCallbackPath, agentgateway.PairConfirmPath:
		return true
	default:
		return false
	}
}
