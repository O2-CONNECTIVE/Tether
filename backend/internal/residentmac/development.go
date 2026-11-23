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

// DevelopmentEnrolledLocation is the fixed per-account location shared by the
// development pairing CLI and resident loader.
func DevelopmentEnrolledLocation(ctx context.Context) (home, policyPath string, err error) {
	home, enrolled, err := EnrolledLocation(ctx)
	if err != nil {
		return "", "", err
	}
	selected, err := developmentPolicy(enrolled)
	if err != nil {
		return "", "", err
	}
	return home, selected, nil
}

// Build-tag-only composition. Loading preserves the real protected loader and
// fixed operation/authentication checks; it neither enrolls nor starts ON.
func LoadDevelopmentStandby(ctx context.Context, options ...StandbyConfig) (*Standby, error) {
	_, selected, err := DevelopmentEnrolledLocation(ctx)
	if err != nil {
		return nil, err
	}
	return LoadStandby(ctx, selected, options...)
}
