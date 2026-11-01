package agenttransport

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"testing"
	"time"
)

// Configuration/cancellation only: no DNS lookup, socket, listener or key.
func TestNativeDialerOwnsGoResolverWithoutGlobalMutation(t *testing.T) {
	global := net.DefaultResolver
	beforePrefer, beforeStrict := global.PreferGo, global.StrictErrors
	a, b := nativeDialer(), nativeDialer()
	if a.Timeout != 10*time.Second || a.Resolver == nil || !a.Resolver.PreferGo || a.Resolver.Dial != nil {
		t.Fatal("wrong scoped resolver")
	}
	if b.Resolver == a.Resolver || a.Resolver == global || b.Resolver == global {
		t.Fatal("shared/global resolver")
	}
	a.Resolver.PreferGo = false
	if !b.Resolver.PreferGo || net.DefaultResolver != global || global.PreferGo != beforePrefer || global.StrictErrors != beforeStrict {
		t.Fatal("global or sibling mutated")
	}
}

// Empty-host validation is guaranteed before resolver I/O. Cancellation is
// covered by the transport lifecycle tests; DNS work may outlive a canceled
// caller, so this test makes no false zero-callback assertion about that case.
func TestNativeResolverRejectsEmptyHost(t *testing.T) {
	d := nativeDialer()
	ips, err := d.Resolver.LookupHost(context.Background(), "")
	if err == nil || len(ips) != 0 {
		t.Fatal("empty host accepted")
	}
}

func TestNativeTransportKeepsTLSAndLifetimeBounds(t *testing.T) {
	tr := nativeTransport(nil).(*http.Transport)
	if tr.Proxy != nil || tr.DialContext == nil || tr.TLSClientConfig == nil || tr.TLSClientConfig.MinVersion != tls.VersionTLS12 || tr.TLSClientConfig.InsecureSkipVerify || tr.TLSHandshakeTimeout != 10*time.Second || tr.ResponseHeaderTimeout != 10*time.Second || !tr.DisableKeepAlives || !tr.DisableCompression || tr.ForceAttemptHTTP2 || tr.MaxConnsPerHost != 1 {
		t.Fatal("transport protection changed")
	}
}
