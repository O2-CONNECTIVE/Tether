//go:build tether_development

package main

import "github.com/O2-CONNECTIVE/rescue-host/backend/internal/residentmac"

func configurePairing(d *deps) {
	d.location = residentmac.DevelopmentEnrolledLocation
	// The development IdP requires an exact registered redirect URI, including
	// the port. A busy port must fail before opening the browser.
	d.callbackAddress = "127.0.0.1:51847"
}
