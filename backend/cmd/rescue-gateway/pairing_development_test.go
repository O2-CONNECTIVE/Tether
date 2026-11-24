//go:build tether_development

package main

import "testing"

func TestDevelopmentPairingKeepsOneStepConfiguration(t *testing.T) {
	// The developer-only build retains the pre-existing CLI pairing path without browser consent settings.
	doc := pairingDoc(t)
	delete(doc["pairing"].(map[string]any), "consent")
	_, err := parseLocalConfig(encode(t, doc), configPath)
	if err != nil {
		t.Fatalf("development pairing config rejected: %v", err)
	}
	if !legacyPairBuildEnabled() {
		t.Fatal("development build did not enable one-step pairing")
	}
}
