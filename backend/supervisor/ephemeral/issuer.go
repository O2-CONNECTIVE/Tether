// Package ephemeral は初期評価用nonresume認可のmemory-only境界。
// Storeの代用品ではなく、保存/復元/自動resumeは提供しない。
// epochは識別子であって認証ではない。正規operator承認、guest拒否、peer/ACLは別依存。
// process残留、Job最後handle、実停止、監督hang/OS sleepの解決は主張しない。
package ephemeral

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io"
	"math"
	"strconv"
	"sync"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/supervisor"
)

var ErrEntropy = errors.New("fresh issuer epochを発行できません")
var ErrExhausted = errors.New("per-boot generationを使い切りました")
var ErrDenied = errors.New("現在のissuer/承認と一致しません")
var ErrBusy = errors.New("現在の承認またはlaunchが完了していません")
var ErrConsumed = errors.New("承認challengeは使用済みです")
var ErrLaunchUnconfirmed = errors.New("launchの結果を確認できません")

// NextGeneration はper-boot番号の候補を計算するだけで、認可やissuer復元ではない。
// 実際の消費はissuerの排他内で行い、wrap/reuseを許さない。
func NextGeneration(previous uint64) (uint64, error) {
	if previous == math.MaxUint64 {
		return 0, ErrExhausted
	}
	return previous + 1, nil
}

type Snapshot struct {
	InstanceEpoch [32]byte
	Generation    uint64
	HasAuthority  bool
	Closed        bool
	Consumed      bool
}

// ApprovedPolicy は保護側が今回の正規operator承認で解決した値だけを渡す。
// Binding/任意path/TTL更新をUIや保存stateから受けず、再起動後は再承認が必要。
type ApprovedPolicy struct {
	PolicyDigest        string
	KeySetDigest        [32]byte
	GuestBoundary       supervisor.GuestBoundary
	HardDeadline        time.Time
	BackendLeaseCeiling time.Duration
}

type state uint8

const (
	pending state = iota
	launching
	active
	revoked
	failed
)

type entry struct {
	owner            *Issuer
	grant            supervisor.ProvisionedGrant
	state            state
	burned, inflight bool
	authority        *supervisor.Authority
	cancel           context.CancelFunc
}

// Challenge はmemory内の単回handle。serialize/restore用fieldを持たない。
// 識別子やこのhandle自体は、operator/peerの認証を代用しない。
type Challenge struct{ entry *entry }

func (c Challenge) Binding() supervisor.Binding {
	if c.entry == nil {
		return supervisor.Binding{}
	}
	return c.entry.grant.Binding
}

// Issuer は初回利用後にコピーしない。lifetimeを閉じてもepoch/counterは再利用しない。
// boundaryは生成時に固定し、後から変更・切替する入口はない。
type Issuer struct {
	mu         sync.Mutex
	epoch      [32]byte
	boundary   supervisor.GuestBoundary
	closed     bool
	generation uint64
	current    *entry
}

// New はauthority lifetimeごとにfresh crypto epochを作り、許可0で始める。
// PID/time/保存UI stateをseedや復元入力に使わない。既存のguest境界専用で、
// CurrentUserNoIsolationのpolicyはPrepareで拒否する。
func New() (*Issuer, error) {
	return newIssuer(supervisor.SeparatedNonAdminGuest)
}

// NewCurrentUser は、既にログオン中の利用者として動く承認だけを発行する別issuer。
// SeparatedNonAdminGuestのpolicyは拒否し、AuthorityはNewCurrentUserで作る。
// 利用者本人の明示同意を得るproducerは別責務で、このissuerは同意を代用しない。
func NewCurrentUser() (*Issuer, error) {
	return newIssuer(supervisor.CurrentUserNoIsolation)
}

func newIssuer(boundary supervisor.GuestBoundary) (*Issuer, error) {
	i := &Issuer{boundary: boundary}
	if _, err := io.ReadFull(rand.Reader, i.epoch[:]); err != nil || i.epoch == ([32]byte{}) {
		return nil, ErrEntropy
	}
	return i, nil
}

// Boundary はこのissuerが発行する唯一の境界を返す。認可判定の代用ではない。
func (i *Issuer) Boundary() supervisor.GuestBoundary {
	if i == nil {
		return 0
	}
	return i.boundary
}

func (i *Issuer) newAuthority(grant supervisor.ProvisionedGrant, now time.Time) (*supervisor.Authority, error) {
	switch i.boundary {
	case supervisor.SeparatedNonAdminGuest:
		return supervisor.New(grant, now)
	case supervisor.CurrentUserNoIsolation:
		return supervisor.NewCurrentUser(grant, now)
	}
	return nil, ErrDenied
}

func (i *Issuer) Snapshot() Snapshot {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.snapshotLocked()
}

// TrySnapshot observes the same actual issuer without waiting for its mutex.
// A nil or busy issuer provides no observation; this does not grant authority.
func (i *Issuer) TrySnapshot() (Snapshot, bool) {
	if i == nil || !i.mu.TryLock() {
		return Snapshot{}, false
	}
	defer i.mu.Unlock()
	return i.snapshotLocked(), true
}

func (i *Issuer) snapshotLocked() Snapshot {
	s := Snapshot{InstanceEpoch: i.epoch, Generation: i.generation, Closed: i.closed}
	if e := i.current; e != nil {
		s.Consumed = e.burned
		s.HasAuthority = e.authority != nil && e.state != revoked && e.state != failed && !i.closed
	}
	// HasAuthorityは存在の観測だけ。現在の認可確認はLookup/coreへ渡す。
	return s
}

func (i *Issuer) Close() error {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.closed = true
	if e := i.current; e != nil {
		i.revoke(e)
		if e.inflight {
			return ErrLaunchUnconfirmed
		}
	}
	return nil
}

// Prepare は保護caller専用。generationを排他内で消費し、既存wireに収まるfresh
// LaunchIDとbackend session Epochを発行する。内部boot epochをIdentity.Epochへ流用しない。
func (i *Issuer) Prepare(p ApprovedPolicy) (Challenge, error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.closed || i.epoch == ([32]byte{}) {
		return Challenge{}, ErrDenied
	}
	// 現在利用者issuerは別境界のpolicyをgeneration消費前に拒否する。既定issuerの
	// Prepareは従来どおりで、guest以外の境界はLaunch内のsupervisor.Newが拒否する。
	if i.boundary == supervisor.CurrentUserNoIsolation && p.GuestBoundary != i.boundary {
		return Challenge{}, ErrDenied
	}
	if e := i.current; e != nil && (e.inflight || e.state != revoked) {
		return Challenge{}, ErrBusy
	}
	next, err := NextGeneration(i.generation)
	if err != nil {
		return Challenge{}, err
	}
	i.generation = next
	var sessionEpoch [8]byte
	if _, err := io.ReadFull(rand.Reader, sessionEpoch[:]); err != nil {
		return Challenge{}, ErrEntropy
	}
	identity := supervisor.Identity{LaunchID: hex.EncodeToString(i.epoch[:]) + "-" + strconv.FormatUint(next, 10), Epoch: strconv.FormatUint(binary.BigEndian.Uint64(sessionEpoch[:]), 10), PolicyDigest: p.PolicyDigest}
	e := &entry{owner: i, grant: supervisor.ProvisionedGrant{Binding: supervisor.Binding{Generation: next, Identity: identity}, KeySetDigest: p.KeySetDigest, GuestBoundary: p.GuestBoundary, HardDeadline: p.HardDeadline, BackendLeaseCeiling: p.BackendLeaseCeiling}}
	i.current = e
	return Challenge{e}, nil
}

// Launch はcallbackの副作用より前にchallengeをlock内でburnする。再試行/保存値復元なし。
// callbackは保護所有者がcontext/core失効を尊重し、実資源を別に所有・回収する責務を持つ。
// callback実行中はlockを保持せず、失効と取消を塞がない。実停止・回収成功は判定しない。
func (i *Issuer) Launch(ctx context.Context, c Challenge, clock func() time.Time, launch func(context.Context, *supervisor.Authority) error) (*supervisor.Authority, error) {
	i.mu.Lock()
	e := c.entry
	if i.closed || i.epoch == ([32]byte{}) || e == nil || e != i.current || e.owner != i {
		i.mu.Unlock()
		return nil, ErrDenied
	}
	if e.burned {
		i.mu.Unlock()
		return nil, ErrConsumed
	}
	e.burned = true
	if ctx == nil || clock == nil || launch == nil || ctx.Err() != nil {
		e.state = failed
		i.mu.Unlock()
		return nil, ErrDenied
	}
	a, err := i.newAuthority(e.grant, readClock(clock))
	if err != nil {
		e.state = failed
		i.mu.Unlock()
		return nil, ErrDenied
	}
	runCtx, cancel := context.WithCancel(ctx)
	e.authority, e.cancel, e.inflight, e.state = a, cancel, true, launching
	i.mu.Unlock()
	err = runLaunch(runCtx, a, launch)
	i.mu.Lock()
	defer i.mu.Unlock()
	e.inflight = false
	if err != nil {
		i.revoke(e)
		e.state = failed
		return nil, ErrLaunchUnconfirmed
	}
	// clock/coreの観測中にも取消が入り得るため、その観測より後にcontextを再確認する。
	observed := a.Snapshot(readClock(clock))
	if i.closed || runCtx.Err() != nil || e != i.current || observed.State != supervisor.Granted {
		i.revoke(e)
		e.state = failed
		return nil, ErrLaunchUnconfirmed
	}
	e.state = active
	return a, nil
}

func readClock(clock func() time.Time) (now time.Time) {
	defer func() {
		if recover() != nil {
			now = time.Time{}
		}
	}()
	return clock()
}
func runLaunch(ctx context.Context, a *supervisor.Authority, launch func(context.Context, *supervisor.Authority) error) (err error) {
	defer func() {
		if recover() != nil {
			err = ErrLaunchUnconfirmed
		}
	}()
	if err := ctx.Err(); err != nil {
		return err
	}
	return launch(ctx, a)
}

func (i *Issuer) revoke(e *entry) {
	e.burned = true
	if e.authority != nil {
		e.authority.Revoke(e.grant.Binding)
	}
	if e.cancel != nil {
		e.cancel()
	}
	if e.state != failed {
		e.state = revoked
	}
}

// Revoke は現在のfull bindingだけを不可逆失効させる。物理停止証明ではない。
func (i *Issuer) Revoke(binding supervisor.Binding) error {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.current == nil || i.current.grant.Binding != binding {
		return ErrDenied
	}
	i.revoke(i.current)
	return nil
}

// Lookup は現在memory mapにあるfull bindingだけを返す。UIpollでrenewしない。
// 古いcallback/保存UIstate/別bootの同generationは、LaunchIDを含む照合で拒否する。
func (i *Issuer) Lookup(binding supervisor.Binding, clock func() time.Time) (*supervisor.Authority, error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	e := i.current
	if i.closed || e == nil || e.state != active || e.authority == nil || e.grant.Binding != binding {
		return nil, ErrDenied
	}
	if clock == nil || e.authority.Snapshot(readClock(clock)).State != supervisor.Granted {
		i.revoke(e)
		return nil, ErrDenied
	}
	return e.authority, nil
}
