//go:build darwin && cgo && ci_actual_producer

package residentpipe

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// Exactly this explicitly named test is live. Compile-only is safe; executing
// requires a disposable CI runner and independent source review. Never run it
// on an interactive desktop to diagnose a failed or unknown cleanup.
func TestCIActualProducerConnection(t *testing.T) {
	if os.Getenv("CI_ACTUAL_PRODUCER") != "1" || os.Getenv("RUNNER_ENVIRONMENT") != "github-hosted" || os.Getenv("RUNNER_OS") != "macOS" || os.Getenv("RUNNER_ARCH") != "ARM64" {
		t.Fatal("requires explicit disposable macOS ARM64 CI lane; no skip")
	}
	binary := os.Getenv("CI_PRODUCER_BINARY")
	expected := os.Getenv("CI_PRODUCER_SHA256")
	raw, err := os.ReadFile(binary)
	if err != nil {
		t.Fatal("fixture operation refused")
	}
	sum := sha256.Sum256(raw)
	if !filepath.IsAbs(binary) || hex.EncodeToString(sum[:]) != expected {
		t.Fatal("helper artifact pin mismatch")
	}
	invocation := os.Getenv("CI_PRODUCER_INVOCATION")
	nonce, nonceErr := hex.DecodeString(invocation)
	if nonceErr != nil || len(nonce) != 16 || hex.EncodeToString(nonce) != invocation {
		t.Fatal("fresh invocation identity required")
	}
	report := os.Getenv("CI_PRODUCER_RESULT")
	if !filepath.IsAbs(report) {
		t.Fatal("explicit external result path required")
	}
	if _, e := os.Lstat(report); !errors.Is(e, os.ErrNotExist) {
		t.Fatal("result path must not exist")
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal("fixture operation refused")
	}
	cwd, err = filepath.EvalSymlinks(cwd)
	if err != nil {
		t.Fatal("fixture operation refused")
	}
	root, err := os.MkdirTemp(cwd, ".ci-producer-")
	if err != nil {
		t.Fatal("fixture operation refused")
	}
	var child *ciChild
	var work *ciWorkChild
	var gateway *ciGateway
	confirmed := false
	diag := &ciDiagnostic{stage: "gateway-setup"}
	// This callback NEVER attempts cleanup after an assertion fails. Persist the
	// failed/unknown outcome, then keep original owners alive until disposable
	// runner teardown. The external observer fails the CI step from this marker.
	t.Cleanup(func() {
		if confirmed {
			return
		}
		stage, code := diag.fields()
		outcome := map[string]any{"stage": stage, "failure_code": code, "outcome": "failed-unknown", "cleanup": "unconfirmed", "test": t.Name(), "invocation": invocation, "helper_sha256": expected, "fixture_retained": true}
		diag.annotate(outcome)
		ciAnnotateNative(outcome, child, diag.commandID)
		data, _ := json.Marshal(outcome)
		if e := ciWriteExclusive(report, append(data, '\n')); e != nil {
			fmt.Fprintln(os.Stderr, "CI_PRODUCER_OUTCOME_WRITE_FAILED")
		}
		fmt.Fprintln(os.Stderr, "CI_PRODUCER_FAILED_UNKNOWN: original owners retained; runner teardown is not cleanup proof")
		for {
			time.Sleep(time.Hour)
			runtime.KeepAlive(child)
			runtime.KeepAlive(work)
			runtime.KeepAlive(gateway)
		}
	})
	gateway = newCIGateway(t, root, diag)
	diag.at("producer-start")
	child, err = startCIChild(binary, root, ciBootstrap{Policy: gateway.policyPath, Endpoint: gateway.endpoint, CA: gateway.caDER})
	if err != nil {
		diag.fail(t, "producer-acquisition")
	}
	diag.at("initial-off")
	if err = child.await(func(s Snapshot) bool {
		return s.Standby == "off" && s.Cleanup == "confirmed" && s.Work == "idle" && s.CanRequestOn
	}, 10*time.Second); err != nil {
		diag.fail(t, ciChildFailure(child, err))
	}
	if err = child.requestStatus(); err != nil {
		diag.fail(t, ciChildFailure(child, err))
	}
	diag.at("on-submit")
	on, err := child.request("on")
	if err != nil {
		diag.fail(t, ciChildFailure(child, err))
	}
	diag.at("on-confirm")
	if err = child.applied(on, "on"); err != nil {
		diag.fail(t, ciChildFailure(child, err))
	}
	diag.at("authenticated")
	if err = child.await(func(s Snapshot) bool { return s.Standby == "on" && s.Registration == "authenticated" }, 10*time.Second); err != nil {
		diag.fail(t, ciChildFailure(child, err))
	}
	generation := gateway.handler.ResidentGeneration(ciHost, ciPrincipal)
	if generation == "" {
		diag.fail(t, "generation-missing")
	}
	tool := gateway.tool
	if child.foreground {
		diag.at("work-cli-start")
		// Keep existing authenticated/principal-isolation negatives. The real
		// CLI then owns one different SID for both work requests and retained reads.
		gateway.initialize()
		work, err = startCIWork(gateway, root)
		if err != nil { diag.fail(t, "work-cli-acquisition") }
		tool = func(name, id string) map[string]any { return work.tool(gateway, name, id) }
	}
	for n, mode := range []string{"natural", "cancel"} {
		diag.at(mode + "-generation")
		if gateway.handler.ResidentGeneration(ciHost, ciPrincipal) != generation {
			diag.fail(t, "generation-changed")
		}
		diag.at(mode + "-initialize")
		if work == nil { gateway.initialize() }
		id := fmt.Sprintf("%032x", n+1)
		diag.at(mode + "-start")
		start := tool("rescue_fixed_start", id)
		if start["request_id"] != id {
			diag.fail(t, "request-correlation")
		}
		diag.at(mode + "-poll")
		if mode == "cancel" {
			diag.at("cancel-await-output")
		}
		canceled := false
		deadline := time.Now().Add(60 * time.Second)
		var value map[string]any
		for time.Now().Before(deadline) {
			value = tool("rescue_fixed_status", id)
			for _, field := range []string{"phase", "command_cleanup", "owner_cleanup", "gateway_cleanup"} {
				if value[field] == "unknown" {
					diag.fail(t, map[string]string{"phase": "work-phase-unknown", "command_cleanup": "command-cleanup-unknown", "owner_cleanup": "owner-cleanup-unknown", "gateway_cleanup": "gateway-cleanup-unknown"}[field])
				}
			}
			if value["error"] != "" {
				code := "work-other-error"
				switch value["error"] {
				case "admission_refused_or_uncertain":
					code = "work-admission-uncertain"
				case "start_uncertain":
					code = "work-start-uncertain"
				case "registration_unobserved":
					code = "work-registration-unobserved"
				case "output_limit":
					code = "work-output-limit"
				}
				diag.fail(t, code)
			}
			output := ciOutput(t, value, diag)
			if mode == "cancel" && !canceled && output == "TETHER_MCP_GUI_BEGIN\n" {
				diag.at("cancel-submit")
				tool("rescue_fixed_cancel", id)
				diag.at("cancel-poll")
				canceled = true
			}
			if value["phase"] == "finished" && value["terminal"] == true {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		diag.at(mode + "-verify")
		if value["phase"] != "finished" || value["terminal"] != true || value["command_id"] == "" || value["request_id"] != id {
			diag.fail(t, "terminal-missing")
		}
		for _, field := range []string{"command_cleanup", "owner_cleanup", "gateway_cleanup"} {
			if value[field] != "confirmed" {
				diag.fail(t, "cleanup-missing")
			}
		}
		expectedOutput := "TETHER_MCP_GUI_BEGIN\nTETHER_MCP_GUI_END\n"
		if mode == "cancel" {
			expectedOutput = "TETHER_MCP_GUI_BEGIN\n"
			if !canceled || value["command_phase"] != "canceled" || value["stop"] != "canceled" {
				diag.fail(t, "cancel-expectation")
			}
		} else {
			exit, ok := value["exit"].(map[string]any)
			if !ok || exit["known"] != true || exit["code"] != float64(0) || value["command_phase"] != "exited" || value["stop"] != "none" {
				diag.fail(t, "natural-expectation")
			}
		}
		if ciOutput(t, value, diag) != expectedOutput {
			diag.fail(t, "output-mismatch")
		}
		if work != nil {
			diag.at("work-cli-release")
			released := tool("rescue_fixed_release", id)
			if released["result_released"] != true || released["phase"] != "finished" || released["owner_cleanup"] != "confirmed" || released["gateway_cleanup"] != "confirmed" || ciOutput(t, released, diag) != "" {
				diag.fail(t, "work-cli-release")
			}
			// Same SID observes its tombstone, never a reset/new generation.
			if tool("rescue_fixed_status", id)["result_released"] != true { diag.fail(t, "work-cli-tombstone") }
		}
	}
	if work != nil {
		diag.at("work-cli-close")
		if work.finish() != nil { diag.fail(t, "work-cli-join") }
	}
	diag.at("off-submit")
	off, err := child.request("off")
	if err != nil {
		diag.fail(t, ciChildFailure(child, err))
	}
	diag.at("off-confirm")
	if err = child.applied(off, "off"); err != nil {
		diag.fail(t, ciChildFailure(child, err))
	}
	diag.at("off-idle")
	if err = child.await(func(s Snapshot) bool {
		return s.Standby == "off" && s.Registration == "disconnected" && s.Work == "idle" && s.Cleanup == "confirmed"
	}, 10*time.Second); err != nil {
		diag.fail(t, ciChildFailure(child, err))
	}
	select {
	case <-child.waitDone:
		diag.fail(t, "child-exited-before-off")
	default:
	}
	diag.at("producer-close")
	if err = child.finish(); err != nil {
		diag.fail(t, ciChildFailure(child, err))
	}
	diag.at("gateway-close")
	if err = gateway.close(); err != nil {
		diag.fail(t, "gateway-join")
	}
	diag.at("fixture-dispose")
	if err = os.RemoveAll(root); err != nil {
		diag.fail(t, "fixture-remove")
	}
	if _, err = os.Lstat(root); !errors.Is(err, os.ErrNotExist) {
		diag.fail(t, "fixture-remains")
	}
	diag.at("complete")
	data, _ := json.Marshal(map[string]any{"stage": "complete", "failure_code": "none", "outcome": "passed", "cleanup": "confirmed", "test": t.Name(), "invocation": invocation, "helper_sha256": expected, "fixture_retained": false, "work_cli_sha256": os.Getenv("CI_WORK_SHA256"), "work_cli_session": work != nil})
	if err = ciWriteExclusive(report, append(data, '\n')); err != nil {
		diag.fail(t, "outcome-persist")
	}
	confirmed = true
}
func ciOutput(t *testing.T, value map[string]any, diag *ciDiagnostic) string {
	t.Helper()
	var text strings.Builder
	chunks, ok := value["chunks"].([]any)
	if !ok {
		diag.fail(t, "output-schema")
	}
	for _, raw := range chunks {
		chunk, ok := raw.(map[string]any)
		if !ok || chunk["encoding"] != "base64" || chunk["stream"] != "stdout" {
			diag.fail(t, "output-schema")
		}
		encoded, ok := chunk["data"].(string)
		if !ok {
			diag.fail(t, "output-schema")
		}
		data, e := base64.StdEncoding.DecodeString(encoded)
		if e != nil {
			diag.fail(t, "output-base64")
		}
		text.Write(data)
	}
	return text.String()
}
