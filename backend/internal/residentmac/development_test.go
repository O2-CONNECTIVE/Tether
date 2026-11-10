//go:build tether_development

package residentmac

import (
	"strings"
	"testing"
)

func TestDevelopmentPolicyIsSeparateAndFixed(t *testing.T) {
	p, err := developmentPolicy("/Users/example/" + enrolledPolicyRelative)
	if err != nil || p != "/Users/example/"+developmentPolicyRelative || strings.Contains(p, "rescue-host.shell") {
		t.Fatalf("separation: %q %v", p, err)
	}
}
func TestDevelopmentPolicyRejectsUnknownHome(t *testing.T) {
	for _, p := range []string{"", enrolledPolicyRelative, "/" + enrolledPolicyRelative, "/Users/example/../example/" + enrolledPolicyRelative, "/Users/example/other.json", "/Users/a\n/" + enrolledPolicyRelative} {
		if _, err := developmentPolicy(p); err == nil {
			t.Fatalf("accepted %q", p)
		}
	}
}
