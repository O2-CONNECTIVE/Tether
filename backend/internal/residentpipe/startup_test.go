package residentpipe

import (
	"errors"
	"fmt"
	"testing"
)

func TestStartupExitCodeRequiresExactEmittedResult(t *testing.T) {
	if ExitCode(nil) != 0 || ExitCode(errStartupRejected) != 2 {
		t.Fatal("exit contract")
	}
	for _, err := range []error{ErrProtocol, errors.New(errStartupRejected.Error()), fmt.Errorf("%w", errStartupRejected), errors.Join(errStartupRejected, ErrProtocol), errors.Join(errStartupRejected)} {
		if ExitCode(err) != 1 {
			t.Fatalf("uncertain result promoted: %v", err)
		}
	}
}
func TestStartupEnvelopeExactWire(t *testing.T) {
	b, err := Encode(startupRejectedEnvelope{1, "startupRejected", 123, "policy-rejected"})
	if err != nil || string(b) != "{\"protocolVersion\":1,\"type\":\"startupRejected\",\"sourceContinuousMs\":123,\"code\":\"policy-rejected\"}\n" {
		t.Fatalf("%s: %v", b, err)
	}
}
