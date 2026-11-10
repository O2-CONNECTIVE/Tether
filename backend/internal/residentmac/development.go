//go:build tether_development

package residentmac

import (
	"context"
	"path"
	"strings"
)

const developmentPolicyRelative = "Library/Application Support/com.o2connective.tether.development/resident-policy.json"

// Separate fixed location: never read or fall back to the production policy.
// The OS-account resolver supplies the home; HOME and runtime paths do not.
func developmentPolicy(enrolled string) (string, error) {
	if !path.IsAbs(enrolled) || path.Clean(enrolled) != enrolled || !strings.HasSuffix(enrolled, "/"+enrolledPolicyRelative) {
		return "", ErrStandbyUnavailable
	}
	home := strings.TrimSuffix(enrolled, "/"+enrolledPolicyRelative)
	if home == "" || home == "/" || strings.ContainsAny(home, "\x00\r\n") {
		return "", ErrStandbyUnavailable
	}
	return home + "/" + developmentPolicyRelative, nil
}

// Build-tag-only composition. Loading preserves the real protected loader and
// fixed operation/authentication checks; it neither enrolls nor starts ON.
func LoadDevelopmentStandby(ctx context.Context) (*Standby, error) {
	enrolled, err := currentEnrolledPolicy(ctx)
	if err != nil {
		return nil, err
	}
	selected, err := developmentPolicy(enrolled)
	if err != nil {
		return nil, err
	}
	return LoadStandby(ctx, selected)
}
