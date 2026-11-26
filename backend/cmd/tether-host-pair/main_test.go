package main

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestParsePairResultRequiresExactJSONMediaTypeAndMembers(t *testing.T) {
	valid := []byte(`{"registration_endpoint":"wss://gw.test/register","host_label":"host","principal_label":"principal","principal_public_key":"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAvzQ9BrCC5xE1lQfM4NXu7CwJwE74lfM2nCHx9+spk8"}`)
	duplicate := strings.Replace(string(valid), `,"host_label"`, `,"registration_endpoint":"wss://gw.test/register","host_label"`, 1)
	unknown := strings.TrimSuffix(string(valid), "}") + `,"extra":true}`
	for _, tc := range []struct{ ct, body string }{
		{"application/jsonp", string(valid)},
		{"application/json", duplicate},
		{"application/json", unknown},
	} {
		if _, err := parsePairResult(tc.ct, []byte(tc.body)); err == nil {
			t.Errorf("accepted content-type=%q body=%s", tc.ct, tc.body)
		}
	}
}

func TestExecuteRejectsInvalidArgumentsWithoutNetworkOrBrowser(t *testing.T) {
	calls := 0
	d := deps{client: http.DefaultClient, location: func(context.Context) (string, string, error) { calls++; return "", "", nil }, openBrowser: func(string) error { calls++; return nil }, callbackWait: time.Millisecond, pairTimeout: time.Millisecond}
	cases := [][]string{{"--gateway", "https://gw.test", "--issuer", "https://id.test"}, {"--gateway", "https://gw.test", "--gateway", "https://gw.test", "--issuer", "https://id.test", "--client-id", "x"}, {"--gateway", "https://", "--issuer", "https://id.test", "--client-id", "x"}, {"--gateway", "http://gw.test", "--issuer", "https://id.test", "--client-id", "x"}, {"--gateway", "https://gw.test/path", "--issuer", "https://id.test", "--client-id", "x"}, {"--gateway", "https://gw.test?x=1", "--issuer", "https://id.test", "--client-id", "x"}, {"--gateway", "HTTPS://gw.test", "--issuer", "https://id.test", "--client-id", "x"}, {"--gateway", "https://GW.test", "--issuer", "https://id.test", "--client-id", "x"}, {"--gateway", "https://gw.test", "--issuer", "https://id.test", "--client-id", "x", "extra"}}
	for _, args := range cases {
		var out, errOut strings.Builder
		if got := execute(context.Background(), args, &out, &errOut, d); got != 2 {
			t.Errorf("args %q status=%d stderr=%q", args, got, errOut.String())
		}
	}
	if calls != 0 {
		t.Fatalf("invalid arguments touched injected services %d times", calls)
	}
}
