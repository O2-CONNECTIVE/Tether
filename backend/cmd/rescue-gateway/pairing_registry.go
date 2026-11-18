package main

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentgateway"
)

var errRegistry = errors.New("rescue-gateway: invalid pairing registry")

type storedPairRecord struct {
	HostLabel    string `json:"label"`
	PublicKey    string `json:"public_key"`
	Fingerprint  string `json:"fingerprint"`
	Issuer       string `json:"issuer"`
	Subject      string `json:"subject"`
	RegisteredAt string `json:"registered_at"`
}

type storedPairRegistry struct {
	Hosts []storedPairRecord `json:"hosts"`
}

// validatePairingRegistry is the validation boundary for persisted pairings.
// Stub: the validation rules are covered by A14 and will be implemented by
// the green change.
func validatePairingRegistry(raw []byte, cfg localConfig) ([]agentgateway.PairRecord, error) {
	var doc storedPairRegistry
	if json.Unmarshal(raw, &doc) != nil {
		return nil, nil
	}
	return nil, nil
}

// openNativeRegistry is a compile-time stub for the unix protected store.
func openNativeRegistry(context.Context, string) (registry, error) {
	return nil, errRegistry
}
