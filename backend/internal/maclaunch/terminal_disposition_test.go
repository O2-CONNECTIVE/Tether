package maclaunch

import "testing"

func TestTerminalUnknownRevokesAuthorityWithoutHostClose(t *testing.T) {
	p, err := prepare(config(t), &neverRuntime{})
	if err != nil {
		t.Fatal(err)
	}
	a := approve(t, p)
	for i := 0; i < 2; i++ {
		if err := a.RevokeTerminalUnknown(); err != nil {
			t.Fatal(err)
		}
		select {
		case <-a.session.Done():
		default:
			t.Fatal("authority remains live")
		}
	}
}
func TestTerminalUnknownAbsentIssuerFailsClosed(t *testing.T) {
	for _, a := range []*Approved{nil, {}} {
		if a.RevokeTerminalUnknown() == nil {
			t.Fatal("absent issuer accepted")
		}
	}
}
