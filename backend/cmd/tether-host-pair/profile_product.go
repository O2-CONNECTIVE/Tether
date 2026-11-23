//go:build !tether_development

package main

import "github.com/O2-CONNECTIVE/rescue-host/backend/internal/residentmac"

func configurePairing(d *deps) {
	d.location = residentmac.EnrolledLocation
}
