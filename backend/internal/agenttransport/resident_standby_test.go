package agenttransport

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentgateway"
)

// standbyFields is the resident_standby message of #412, written here from the
// spec and not with the product encoder, so the test pins the wire format:
// the host key signs the binding under "host-standby" and the four values in
// this order.
func standbyFields(t *testing.T, b residentBinding, key ssh.Signer, role string, v map[string]string) map[string]string {
	t.Helper()
	sig, e := sign(key, key.PublicKey(), residentTranscript(b, role, v["generation"], v["revision"], v["standby"], v["until_unix_ms"]))
	if e != nil {
		t.Fatal(e)
	}
	m := map[string]string{"type": "resident_standby", "signature": b64.EncodeToString(sig)}
	for k, x := range v {
		m[k] = x
	}
	return m
}

func sameStandby(a, b agentgateway.ResidentStandby) bool {
	return a.Revision == b.Revision && a.Standby == b.Standby && a.Until.Equal(b.Until)
}

// The AI reads how long standby has left from the gateway, so the gateway must
// hold what the Mac's own control last said, and only for that control.
func TestResidentStandbyReportReachesOnlyItsOwnControl(t *testing.T) {
	r := connectorRig(t)
	done := residentRun(r.c)
	s := takeSource(t, r)
	gen := s.Receiver().Generation()
	// A live control that has not reported reads as live, with no report, so
	// the AI is told "not reported", not "control ended" (#412 review).
	if v, ok := r.h.ResidentStandby(hostLabel, member1, gen); !ok || v.Revision != 0 {
		t.Fatal("standby before the Mac reported it:", v, ok)
	}
	until := time.Now().Add(90*time.Minute + 1500*time.Millisecond)
	if e := s.ReportStandby(agentgateway.ResidentStandby{Revision: 7, Standby: "on", Until: until}); e != nil {
		t.Fatal(e)
	}
	// The wire carries milliseconds, as every other resident deadline does.
	on := agentgateway.ResidentStandby{Revision: 7, Standby: "on", Until: time.UnixMilli(until.UnixMilli())}
	verticalEventually(t, func() bool { v, ok := r.h.ResidentStandby(hostLabel, member1, gen); return ok && sameStandby(v, on) })
	for _, q := range [][3]string{{hostLabel, member1, strings.Repeat("0", 64)}, {hostLabel, member2, gen}, {otherHost, member1, gen}} {
		if _, ok := r.h.ResidentStandby(q[0], q[1], q[2]); ok {
			t.Fatalf("standby of %s/%s/%s read for another control", q[0], q[1], q[2])
		}
	}
	// Stopping standby publishes no end.
	if e := s.ReportStandby(agentgateway.ResidentStandby{Revision: 9, Standby: "stopping"}); e != nil {
		t.Fatal(e)
	}
	stopping := agentgateway.ResidentStandby{Revision: 9, Standby: "stopping"}
	verticalEventually(t, func() bool {
		v, ok := r.h.ResidentStandby(hostLabel, member1, gen)
		return ok && sameStandby(v, stopping)
	})
	r.c.Stop()
	if e := finishSource(t, s, nil); e != nil {
		t.Fatal(e)
	}
	if e := await(t, done, 3*time.Second); !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
	verticalEventually(t, func() bool { _, ok := r.h.ResidentStandby(hostLabel, member1, gen); return !ok })
}

// The Mac signs only what its standby can be: on, or stopping without an end.
func TestResidentStandbyMacSignsOnlyACoherentReport(t *testing.T) {
	r := connectorRig(t)
	residentRun(r.c)
	s := takeSource(t, r)
	end := time.Now().Add(time.Hour)
	for _, v := range []agentgateway.ResidentStandby{
		{Revision: 0, Standby: "on", Until: end},
		{Revision: 1, Standby: "off"},
		{Revision: 1, Standby: "unknown"},
		{Revision: 1, Standby: "stopping", Until: end},
		{Revision: 1, Standby: "on", Until: time.UnixMilli(123)},
	} {
		if e := s.ReportStandby(v); !errors.Is(e, ErrRefused) {
			t.Fatalf("signed %+v: %v", v, e)
		}
	}
}

// A report the gateway did not get from this control's host, or one older than
// what it holds, is dropped. Dropping is all: the report carries no authority,
// so the control and its work go on.
func TestResidentStandbyGatewayDropsForgedStaleAndIncoherentReports(t *testing.T) {
	r := connectorRig(t)
	residentRun(r.c)
	s := takeSource(t, r)
	b, host := s.receiver.binding, s.receiver.cfg.HostKey
	gen := b.generation
	end := time.Now().Add(time.Hour).UnixMilli()
	later := end + int64(24*time.Hour/time.Millisecond)
	value := func(revision uint64, until int64) map[string]string {
		return map[string]string{"generation": gen, "revision": strconv.FormatUint(revision, 10), "standby": "on", "until_unix_ms": strconv.FormatInt(until, 10)}
	}
	write := func(m map[string]string) {
		t.Helper()
		ctx, stop := context.WithTimeout(context.Background(), 3*time.Second)
		defer stop()
		if e := residentWrite(ctx, s.receiver.ws, residentJSON(m)); e != nil {
			t.Fatal(e)
		}
	}
	held := agentgateway.ResidentStandby{Revision: 5, Standby: "on", Until: time.UnixMilli(end)}
	write(standbyFields(t, b, host, "host-standby", value(5, end)))
	verticalEventually(t, func() bool { v, ok := r.h.ResidentStandby(hostLabel, member1, gen); return ok && sameStandby(v, held) })

	// Every dropped report names a later end, so adopting any one would show.
	tampered := standbyFields(t, b, host, "host-standby", value(9, end))
	tampered["until_unix_ms"] = strconv.FormatInt(later, 10)
	otherGeneration := value(9, later)
	otherGeneration["generation"] = strings.Repeat("0", 64)
	otherControl := b
	otherControl.serverNonce = bytes.Repeat([]byte{0x77}, len(b.serverNonce))
	extraField := standbyFields(t, b, host, "host-standby", value(9, later))
	extraField["hours"] = "24"
	// The first draft's expired field is gone from the wire (#412 review).
	oldField := standbyFields(t, b, host, "host-standby", value(9, later))
	oldField["expired"] = "false"
	incoherent := func(edit func(map[string]string)) map[string]string {
		v := value(9, later)
		edit(v)
		return standbyFields(t, b, host, "host-standby", v)
	}
	for _, m := range map[string]map[string]string{
		"another key":         standbyFields(t, b, fixtureSigner(t, 0x31), "host-standby", value(9, later)),
		"the gateway's key":   standbyFields(t, b, fixtureSigner(t, 0x21), "host-standby", value(9, later)),
		"another role":        standbyFields(t, b, host, "host-pending", value(9, later)),
		"tampered after sign": tampered,
		"another generation":  standbyFields(t, b, host, "host-standby", otherGeneration),
		"another control":     standbyFields(t, otherControl, host, "host-standby", value(9, later)),
		"an extra field":      extraField,
		"the old field":       oldField,
		"the same revision":   standbyFields(t, b, host, "host-standby", value(5, later)),
		"an older revision":   standbyFields(t, b, host, "host-standby", value(4, later)),
		"revision zero":       incoherent(func(v map[string]string) { v["revision"] = "0" }),
		"padded revision":     incoherent(func(v map[string]string) { v["revision"] = "09" }),
		"standby off":         incoherent(func(v map[string]string) { v["standby"] = "off" }),
		"stopping with end":   incoherent(func(v map[string]string) { v["standby"] = "stopping" }),
		"short end":           incoherent(func(v map[string]string) { v["until_unix_ms"] = "123" }),
	} {
		write(m)
	}
	// The host's refusal of an intent comes after every report above on the
	// same ordered control, so its arrival proves they were all read.
	ctx, stop := context.WithTimeout(context.Background(), 3*time.Second)
	defer stop()
	result := make(chan error, 1)
	go func() {
		result <- r.h.ProposeResidentFixed(ctx, hostLabel, member1, fmt.Sprintf("%032x", 1), time.Now().Add(2*time.Second).Truncate(time.Millisecond))
	}()
	i, e := s.Next()
	if e != nil {
		t.Fatal("control ended on a dropped report", e)
	}
	if e = s.Refuse(i.Correlation()); e != nil {
		t.Fatal(e)
	}
	if e = await(t, result, 3*time.Second); !errors.Is(e, ErrWorkUnconfigured) {
		t.Fatal("control did not survive the dropped reports", e)
	}
	if v, ok := r.h.ResidentStandby(hostLabel, member1, gen); !ok || !sameStandby(v, held) {
		t.Fatalf("adopted a dropped report: %+v", v)
	}
	// A later genuine report is still taken.
	write(standbyFields(t, b, host, "host-standby", value(6, later)))
	next := agentgateway.ResidentStandby{Revision: 6, Standby: "on", Until: time.UnixMilli(later)}
	verticalEventually(t, func() bool { v, ok := r.h.ResidentStandby(hostLabel, member1, gen); return ok && sameStandby(v, next) })
}
