//go:build !unix

package main

import (
	"context"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
)

type unsupportedOSCountingTransport struct{ requests int }

func (r *unsupportedOSCountingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	r.requests++
	return nil, nil
}

func TestExecuteUnsupportedOSStopsBeforeBrowserOrNetwork(t *testing.T) {
	home := t.TempDir()
	transport := &unsupportedOSCountingTransport{}
	browserCalls, protectedReads := 0, 0
	d := deps{
		client: &http.Client{Transport: transport},
		location: func(context.Context) (string, string, error) {
			return home, filepath.Join(home, "resident-policy.json"), nil
		},
		readProtected: func(context.Context, string) ([]byte, error) {
			protectedReads++
			return nil, nil
		},
		openBrowser: func(string) error {
			browserCalls++
			return nil
		},
	}
	var stdout, stderr strings.Builder
	args := []string{"--gateway", "https://gw.test", "--issuer", "https://id.test", "--client-id", "client"}
	if code := execute(context.Background(), args, &stdout, &stderr, d); code != 1 || !strings.Contains(stderr.String(), diagUnsupportedOS) || browserCalls != 0 || transport.requests != 0 || protectedReads != 0 {
		t.Fatalf("status=%d browser=%d requests=%d protected reads=%d stderr=%q", code, browserCalls, transport.requests, protectedReads, stderr.String())
	}
}
