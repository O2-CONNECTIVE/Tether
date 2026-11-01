// Package supervisor は将来の保護サービス向けの純粋な grant 判定を扱う。
// サービス登録、IPC、timer、通信、物理終了の確認は提供しない。
// hard ceiling はここが持ち、Go Session はそれを超えない短 lease と実通信を持つ。
// Windows service identity/token・IPC 認証・承認経路は未決であり、配線しない。
// 保護 caller は時刻を供給し、IPC 喪失等で Revoke し、独立に期限執行と回収を行う。
// 呼出しがなければ状態は進まない。親/監督 hang・OS sleep 中の執行は保証しない。
// この型は製品 IPC schema ではなく、製品 gate や既存受入判定も変更しない。
package supervisor

import (
	"errors"
	"sync"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/lease"
)

var ErrDenied = errors.New("supervisor grant または期限条件が一致しません")
var ErrClaimed = errors.New("supervisor grant は既に別の利用者が専有しています")

// Identity は Go Session と同じ同一性を使う。別の wire 定義ではない。
type Identity = lease.Identity

type Binding struct {
	// 保護所有者のnamespace内で再利用しない番号。起動を跨ぐ同一性は番号単独でなく、
	// ownerのinstance epoch + Generation + full Identity全体で非再利用を保証する。
	// nonresumeではfresh namespace/LaunchIDと許可0からの再承認を使う。
	// Identity.Epochはbackend session epochで、instance epochへ読み替えない。
	// このcoreは値を生成・increment・wrap・復元しない。
	Generation uint64
	Identity   Identity
}

// GuestBoundary は保護 provisioning による表明。実 OS 検証の代わりにはならない。
type GuestBoundary uint8

const SeparatedNonAdminGuest GuestBoundary = 1

// CurrentUserNoIsolation は、既にログオン中の利用者として動くことを承認した表明。
// guest分離・権限分離はなく、その利用者の資源へ到達できる。既定のNewと既存の
// guest前提consumerはこの値を拒否し、NewCurrentUserだけが受け付ける。
const CurrentUserNoIsolation GuestBoundary = 2

// ProvisionedGrant は保護 provisioning が検証・承認した値だけを渡す入力。
// UI、wire、保存 snapshot をこの型へ直接変換してはならない。
// GuestBoundary と KeySetDigest は OS 分離や実鍵集合の証明ではない。
type ProvisionedGrant struct {
	Binding             Binding
	KeySetDigest        [32]byte
	GuestBoundary       GuestBoundary
	HardDeadline        time.Time
	BackendLeaseCeiling time.Duration
}

type State string

const (
	Granted State = "granted"
	Revoked State = "revoked"
)

type Reason string

const (
	HardDeadlineReached Reason = "hard-deadline"
	ClockUnavailable    Reason = "clock-unavailable"
	ClockRegressed      Reason = "clock-regression"
	Uninitialized       Reason = "uninitialized"
	ExplicitStop        Reason = "explicit-stop"
)

type Snapshot struct {
	Grant  ProvisionedGrant
	State  State
	Reason Reason
}

// Authority は1承認につき1つ保持する。初回利用後にコピーしてはならない。
type Authority struct {
	mu      sync.Mutex
	grant   ProvisionedGrant
	state   State
	reason  Reason
	last    time.Time
	done    chan struct{} // 最初のRevoked遷移で一度だけ閉じる
	ended   bool
	claimed bool // 単回の専有。Closeや失効で戻さない
}

// New は新規承認にだけ使う。再起動時に旧 grant を再生成する入口ではない。
// compound identity非再利用と新規承認の認証は保護所有者の必須責務。
// 永続modeの失効記録と、nonresumeのfresh起動/許可全破棄を暗黙に代替しない。
// この core だけでは再起動を跨いだ replay 防止を保証しない。
// 受け付ける境界はSeparatedNonAdminGuestだけで、CurrentUserNoIsolationは拒否する。
func New(grant ProvisionedGrant, now time.Time) (*Authority, error) {
	return newAuthority(grant, now, SeparatedNonAdminGuest)
}

// NewCurrentUser は現在利用者として動く承認専用の明示入口。guest境界を受け付けず、
// 期限・世代・失効の規則はNewと同じ。承認の正当性（利用者本人の同意）は呼出し側の責務。
func NewCurrentUser(grant ProvisionedGrant, now time.Time) (*Authority, error) {
	return newAuthority(grant, now, CurrentUserNoIsolation)
}

func newAuthority(grant ProvisionedGrant, now time.Time, boundary GuestBoundary) (*Authority, error) {
	now = now.Round(0)
	grant.HardDeadline = grant.HardDeadline.Round(0)
	if grant.Binding.Generation == 0 || !grant.Binding.Identity.Valid() || grant.KeySetDigest == ([32]byte{}) ||
		grant.GuestBoundary != boundary || now.IsZero() || grant.HardDeadline.IsZero() ||
		grant.BackendLeaseCeiling <= 0 || !now.Add(grant.BackendLeaseCeiling).Before(grant.HardDeadline) {
		return nil, ErrDenied
	}
	return &Authority{grant: grant, state: Granted, last: now, done: make(chan struct{})}, nil
}

// endLocked closes Done once after the first transition out of Granted.
func (a *Authority) endLocked() {
	if a.done == nil {
		a.done = make(chan struct{})
	}
	if a.state != Granted && !a.ended {
		a.ended = true
		close(a.done)
	}
}

// Done は同じAuthorityの失効（明示Revoke、観測した期限・時計異常）で閉じる通知。
// timerではないため、未観測のhard deadlineだけでは閉じない。期限執行は保護callerが持つ。
// 呼出し側のcontextやlaunch callbackの寿命とは独立で、Grantedへ戻ることはない。
func (a *Authority) Done() <-chan struct{} {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.state == "" {
		a.state, a.reason = Revoked, Uninitialized
	}
	a.endLocked()
	return a.done
}

// Grant は不変のgrant値を時刻観測なしで返す。状態・期限の判定や認可には使わない。
// 別goroutineの時刻読取りとの順序逆転で時計巻戻りと誤観測しないためのもの。
func (a *Authority) Grant() ProvisionedGrant {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.grant
}

// Status は最後に観測済みの状態と理由を、時刻を新たに観測せずに返す。
// 失効の理由を読むためのもので、期限の判定や認可には使わない。別goroutineが
// 先に読んだ時刻で観測し直して、時計の巻戻りと誤判定することを避ける。
func (a *Authority) Status() (State, Reason) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.state == "" {
		return Revoked, Uninitialized
	}
	return a.state, a.reason
}

// ClaimConsumer は同じ有効grantを1つの保護consumerへ単回専有させる。
// 解放・再専有はなく、失効後も戻さない。認可の追加や期限延長ではない。
// 時刻は観測しないため、期限の確認は呼出し側が直前のSnapshot/Lookupで行う。
func (a *Authority) ClaimConsumer(binding Binding) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.grant.Binding.Generation == 0 || binding != a.grant.Binding {
		return ErrDenied
	}
	if a.state != Granted {
		return ErrDenied
	}
	if a.claimed {
		return ErrClaimed
	}
	a.claimed = true
	return nil
}

func (a *Authority) observe(now time.Time) {
	// 壁時計の巻戻りも観測するため、埋め込まれた monotonic 値には依存しない。
	// now は未信頼入力ではなく、保護 caller が順序を保証した時刻値に限定する。
	now = now.Round(0)
	if a.state == "" {
		a.state, a.reason = Revoked, Uninitialized
	}
	if a.state == Granted {
		switch {
		case now.IsZero():
			a.state, a.reason = Revoked, ClockUnavailable
		case now.Before(a.last):
			a.state, a.reason = Revoked, ClockRegressed
		case !now.Before(a.grant.HardDeadline):
			a.state, a.reason = Revoked, HardDeadlineReached
		}
		a.last = now
	}
	a.endLocked()
}

// Snapshot は期限を再判定するが、新規認可・lease 更新・期限延長はしない。
func (a *Authority) Snapshot(now time.Time) Snapshot {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.observe(now)
	return Snapshot{Grant: a.grant, State: a.state, Reason: a.reason}
}

// Revoke は同じ grant だけを不可逆に失効させる。通信終了の確認ではない。
func (a *Authority) Revoke(binding Binding) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.grant.Binding.Generation == 0 || binding != a.grant.Binding {
		return ErrDenied
	}
	if a.state == Granted {
		a.state, a.reason = Revoked, ExplicitStop
	}
	a.endLocked()
	return nil
}

// CheckBackendLease は保護 caller の提案期限が上限内かだけを判定する。
// 成功は更新者の認証・連番検証・更新承認ではなく、lease を発行/更新しない。
// UI の poll をこの結果で Session.Renew へ接続してはならない。
func (a *Authority) CheckBackendLease(binding Binding, deadline, now time.Time) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if binding != a.grant.Binding {
		return ErrDenied
	}
	a.observe(now)
	if a.state != Granted {
		return ErrDenied
	}
	now, deadline = now.Round(0), deadline.Round(0)
	if !deadline.After(now) || deadline.After(now.Add(a.grant.BackendLeaseCeiling)) || deadline.After(a.grant.HardDeadline) {
		return ErrDenied
	}
	return nil
}
