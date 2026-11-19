package maclaunch

import (
	"net"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentchannel"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentexec"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/macagent"
	"golang.org/x/crypto/ssh"
)

// The host's rescue permission state machine: 受付中 → 確認中 → セッション中 →
// 終了 (docs/rescue-connection-start.md, 3. 許可と拒否, ホストの状態, 期限とホスト側の切断).
// Only a reply from the native helper approves it; it is the second approval
// source of this package beside the controlling terminal. It is for tests at
// the current boundary (current user, no isolation) and is not enabled as a
// product: the Mac guest account is not designed yet.

// RescueDurations are the selectable grant lifetimes from the time of
// approval (#399). A zero RescueConfig.Duration selects RescueDefaultDuration.
var RescueDurations = [...]time.Duration{2 * time.Hour, 4 * time.Hour, 8 * time.Hour, 12 * time.Hour, 24 * time.Hour}

const (
	RescueDefaultDuration = 2 * time.Hour
	// RescuePromptTimeout is the automatic refusal, counted from Show on both
	// the monotonic and the wall clock.
	RescuePromptTimeout = 60 * time.Second
	// RescueWarning is shown verbatim in every prompt; the digest covers it.
	RescueWarning = "隔離なし: 許可すると、担当者がこの Mac の現在のユーザーとして任意のコマンドを実行でき、ファイル・鍵・ネットワークにアクセスできます。"
)

// RescueRequest is a rescue request whose identity proof was already verified
// against the pinned identity keys, this reception id and K. Its fields are
// unexported: only that verification (after #423) and this package's tests
// construct it.
type RescueRequest struct {
	client                             ssh.PublicKey // C
	displayName, organization, profile string
}

// RescuePrompt is what the native helper displays.
type RescuePrompt struct {
	RequestID         string // fresh per request, chosen by the host
	Organization      string // 「{組織} が確認: {表示名}」
	DisplayName       string
	Profile           string        // 操作範囲
	Duration          time.Duration // lifetime from the time of approval
	ClientFingerprint string        // ssh.FingerprintSHA256 of C
	Warning           string        // RescueWarning
}

// Digest is SHA-256 over the domain "rescue-host.maclaunch.rescue-prompt\x00v1\x00"
// followed by each field in declaration order as a 4-byte big-endian length and
// its bytes; Duration is its whole seconds in decimal. The helper returns the
// digest of what it displayed.
func (p RescuePrompt) Digest() [32]byte { return [32]byte{} }

// RescueHelper is the native helper's connection as Go sees it. #410
// authenticates that connection; a WebView IPC handler never holds it.
type RescueHelper interface {
	// Show hands p to the helper. It does not wait for the user's answer.
	Show(RescuePrompt) error
	// Withdraw removes the prompt after a timeout, reception loss or host-side end.
	Withdraw(requestID string)
}

// RescueReply is the helper's answer to one prompt.
type RescueReply struct {
	RequestID string
	Digest    [32]byte
	Allow     bool
}

// RescueReception is the host end of the reception connection, owner of K.
type RescueReception interface {
	// Done closes when the reception connection ends, including code expiry.
	Done() <-chan struct{}
	// Refuse returns a K-signed deny, or timeout when timedOut.
	Refuse(timedOut bool) error
	// Allow returns a K-signed allow with d and the stream that now carries the channel.
	Allow(d Dispatch) (net.Conn, error)
	// Close ends the reception and discards K.
	Close() error
}

// RescueConfig is supplied at 受付 ON.
type RescueConfig struct {
	HostKey        ssh.Signer // K, the channel host key
	Runtime        macagent.Config
	Duration       time.Duration
	CleanupTimeout time.Duration
	Budgets        agentchannel.Budgets
	Helper         RescueHelper
	Reception      RescueReception
}

// Rescue owns one reception from 受付中 to 終了. It never restarts the reception.
type Rescue struct{}

type rescueClock struct {
	wall func() time.Time
	mono func() time.Duration // elapsed on a monotonic clock
	tick <-chan time.Time     // rechecks the 60-second prompt and the grant deadline
}

// NewRescue validates the configuration and the local Mac runtime in 受付中.
func NewRescue(cfg RescueConfig) (*Rescue, error) {
	if !localSupported() {
		return nil, ErrConfig
	}
	runtime, err := macagent.New(cfg.Runtime)
	if err != nil {
		return nil, ErrConfig
	}
	return newRescue(cfg, runtime, rescueClock{})
}

func newRescue(cfg RescueConfig, runtime agentexec.ProfiledRuntime, clock rescueClock) (*Rescue, error) {
	return &Rescue{}, nil
}

// Receive moves 受付中 to 確認中 and returns after Show. Any other state
// refuses without a prompt.
func (r *Rescue) Receive(req RescueRequest) error { return ErrDenied }

// Answer accepts reply only from the helper this Rescue was built with, for the
// current prompt, within RescuePromptTimeout. Allow issues the grant.
func (r *Rescue) Answer(from RescueHelper, reply RescueReply) error { return ErrDenied }

// Stop is a host-side end: the stop operation, 受付 OFF, app exit, logout or restart.
func (r *Rescue) Stop() {}

// Done closes after the end: the grant revoked and reclaimed, K discarded.
func (r *Rescue) Done() <-chan struct{} {
	done := make(chan struct{})
	close(done)
	return done
}
