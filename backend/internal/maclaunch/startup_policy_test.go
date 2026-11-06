package maclaunch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentkeys"
	"golang.org/x/crypto/ssh"
	"testing"
	"time"
)

func TestStandbyPolicyRejectedBeforeAnyKeyAcquisition(t *testing.T) {
	for _, kind := range []string{"schema", "id", "command", "timeout", "extra", "same-key-path"} {
		t.Run(kind, func(t *testing.T) {
			f, _ := fixedNativeFixture(t)
			d := f.deps
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			raw, err := d.read(ctx, f.path)
			if err != nil {
				t.Fatal(err)
			}
			var p map[string]any
			if json.Unmarshal(raw, &p) != nil {
				t.Fatal("fixture")
			}
			switch kind {
			case "schema":
				p = map[string]any{}
			case "same-key-path":
				p["host_key_path"] = f.path
			default:
				ops := p["operations"].([]any)
				op := ops[0].(map[string]any)
				switch kind {
				case "id":
					op["id"] = "other"
				case "command":
					op["command"] = "true"
				case "timeout":
					op["timeout_ms"] = 14000
				case "extra":
					ops = append(ops, map[string]any{"id": "extra", "command": "true", "timeout_ms": 1000})
				}
				p["operations"] = ops
			}
			raw, _ = json.Marshal(p)
			d.read = func(context.Context, string) ([]byte, error) { return raw, nil }
			keyCalls := 0
			d.key = func(context.Context, agentkeys.Config) (ssh.Signer, error) {
				keyCalls++
				return nil, errors.New("must not read key")
			}
			got, err := loadResidentMode(ctx, f.path, d, true)
			if got != nil || !IsStartupPolicyRejected(err) || keyCalls != 0 {
				t.Fatalf("factory=%v err=%v keys=%d", got, err, keyCalls)
			}
			for _, b := range raw {
				if b != 0 {
					t.Fatal("policy buffer not cleared before result")
				}
			}
		})
	}
}

func TestStandbyReaderAndKeyErrorsNeverMintRejection(t *testing.T) {
	failures := []error{agentkeys.ErrConfig, agentkeys.ErrUnsupported, agentkeys.ErrDenied, agentkeys.ErrCleanup, ErrResidentUnknown, context.DeadlineExceeded, errors.New("unclassified"), startupPolicyRejected{}, errors.Join(startupPolicyRejected{}, agentkeys.ErrCleanup)}
	for _, phase := range []string{"read", "key"} {
		for i, failure := range failures {
			t.Run(fmt.Sprintf("%s-%d", phase, i), func(t *testing.T) {
				f, _ := fixedNativeFixture(t)
				d := f.deps
				if phase == "read" {
					d.read = func(context.Context, string) ([]byte, error) { return []byte("{}"), failure }
				} else {
					d.key = func(context.Context, agentkeys.Config) (ssh.Signer, error) { return nil, failure }
				}
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				got, err := loadResidentMode(ctx, f.path, d, true)
				if got != nil || err == nil || IsStartupPolicyRejected(err) {
					t.Fatalf("uncertain result classified: %v", err)
				}
				if errors.Is(failure, agentkeys.ErrCleanup) && !errors.Is(err, ErrResidentUnknown) {
					t.Fatal("cleanup uncertainty lost")
				}
			})
		}
	}
}

func TestStandbyPositiveClassificationIsDirectOnly(t *testing.T) {
	clean := startupPolicyRejected{}
	for _, err := range []error{nil, ErrResident, ErrResidentUnknown, errors.New(clean.Error()), fmt.Errorf("wrap: %w", clean), errors.Join(clean, agentkeys.ErrCleanup), errors.Join(clean, ErrResidentUnknown), errors.Join(clean)} {
		if IsStartupPolicyRejected(err) {
			t.Fatalf("non-direct result accepted: %v", err)
		}
	}
	if !IsStartupPolicyRejected(clean) {
		t.Fatal("direct private type refused")
	}
}
func TestStandbyLoaderPanicsNeverBecomeRejection(t *testing.T) {
	for _, phase := range []string{"read", "key"} {
		t.Run(phase, func(t *testing.T) {
			f, _ := fixedNativeFixture(t)
			d := f.deps
			if phase == "read" {
				d.read = func(context.Context, string) ([]byte, error) { panic("test read") }
			} else {
				d.key = func(context.Context, agentkeys.Config) (ssh.Signer, error) { panic("test key") }
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			defer func() {
				if recover() == nil {
					t.Error("panic was converted or suppressed")
				}
			}()
			_, err := loadResidentMode(ctx, f.path, d, true)
			t.Fatalf("panic unexpectedly returned: %v", err)
		})
	}
}
func TestStandbyPreReadAndKeyValidationAreNotPositive(t *testing.T) {
	f, _ := fixedNativeFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := loadResidentMode(ctx, "relative", f.deps, true); IsStartupPolicyRejected(err) {
		t.Fatal("pre-read result positive")
	}
	d := f.deps
	d.key = func(context.Context, agentkeys.Config) (ssh.Signer, error) { return nil, nil }
	if _, err := loadResidentMode(ctx, f.path, d, true); err == nil || IsStartupPolicyRejected(err) {
		t.Fatal("nil signer positive")
	}
}
