package grantstore

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/supervisor"
)

var (
	ErrUnconfigured = errors.New("保護された grant store は未設定です")
	ErrUnavailable  = errors.New("grant store を排他取得できません")
	ErrSpent        = errors.New("grant slot は使用済みまたは未完了です")
	ErrRejected     = errors.New("slot と承認値が一致しません")
	ErrUsed         = errors.New("issuer は使用済みです")
	ErrUnconfirmed  = errors.New("grant store の永続化結果を確認できません")
)

// Store は保護 provisioning だけが解決する固定slot。path/URLは受け取らない。
// Acquire は真正性/非rollbackを確認し、排他かつ crash 後も残る durable writer claim
// を取得してから返す。kernel lockだけでclaimを代用しない。本番実装は提供しない。
type Store interface {
	Acquire(context.Context) (Lease, error)
}

// Lease はプロセス間排他と同時更新を所有する保護 backend の契約。
// Read は RecordSize+1 まで。Commit は expected revision を比較し、atomic replace と
// file/parent directory 等の必要な fsync を完了してからのみ成功を返す。
// Windows/Unix 別の rename/replace、書込順序、排他・ACLは未実装・未検証。
// 任意 error は結果不明として扱う。Releaseだけがdurable claimを正常解除できる。
// Quarantine は process資源を閉じ、claimを永続的に残す。両者は再呼出し安全が必要。
type Lease interface {
	Read(context.Context) ([]byte, error)
	Commit(context.Context, uint64, []byte) error
	Release() error
	Quarantine() error
}

// Issuer は1承認1slotを消費する。初回利用後のコピー・再利用は禁止。
// 新世代発行、slotの真正性/削除/複製/巻戻し検出は別の保護信頼根の必須責務。
// 使用済slotからAuthorityを復元せず、resetや自動recoveryは提供しない。
type Issuer struct {
	mu                          sync.Mutex
	lease                       Lease
	record                      Record
	authority                   *supervisor.Authority
	binding                     supervisor.Binding
	attempted, closed, poisoned bool
}

// OpenProtected は信頼根・保護ディレクトリ・OS耐久書込み未設定のため常に閉じる。
// codecやtempdir試験をACL/authenticity保護済みのfilesystem driverと見なさない。
func OpenProtected(context.Context) (*Issuer, error) { return nil, ErrUnconfigured }

func Open(ctx context.Context, store Store) (*Issuer, error) {
	if store == nil {
		return nil, ErrUnconfigured
	}
	lease, err := store.Acquire(ctx)
	if err != nil || lease == nil {
		if lease != nil {
			lease.Quarantine()
		}
		return nil, ErrUnavailable
	}
	data, err := lease.Read(ctx)
	if err != nil {
		lease.Quarantine()
		return nil, ErrUnconfirmed
	}
	record, err := DecodeRecord(data)
	if err != nil {
		lease.Quarantine()
		return nil, ErrInvalidRecord
	}
	if record.Phase != Ready {
		lease.Quarantine()
		return nil, ErrSpent
	}
	return &Issuer{lease: lease, record: record}, nil
}

func (i *Issuer) poison() error {
	// 発行済みの権威を、storage/claim に触るより先に必ず失効させる。
	if i.authority != nil {
		i.authority.Revoke(i.binding)
	}
	i.poisoned = true
	i.lease.Quarantine()
	return ErrUnconfirmed
}

func (i *Issuer) commit(ctx context.Context, phase Phase) error {
	next := i.record
	next.Phase = phase
	next.Revision = uint64(phase)
	data, err := EncodeRecord(next)
	if err != nil {
		return i.poison()
	}
	if err := i.lease.Commit(ctx, i.record.Revision, data); err != nil {
		return i.poison()
	}
	i.record = next
	return nil
}

// Authorize は2つの永続commit完了後だけ既存coreを作る。hardcapの権威はcoreに残す。
// grant/clockは保護caller由来だけ。clockはcommit後に読み、古い時刻を持ち越さない。
// UI入力や保存snapshotからの再承認には使わない。clock故障も認可しない。
func (i *Issuer) Authorize(ctx context.Context, grant supervisor.ProvisionedGrant, clock func() time.Time) (*supervisor.Authority, error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.lease == nil {
		return nil, ErrUnconfigured
	}
	if i.closed || i.poisoned || i.attempted {
		return nil, ErrUsed
	}
	i.attempted = true
	digest, err := fingerprint(grant)
	if err != nil || clock == nil || grant.Binding.Generation != i.record.Generation || digest != i.record.GrantDigest {
		i.poison()
		return nil, ErrRejected
	}
	if err := i.commit(ctx, Intent); err != nil {
		return nil, err
	}
	if err := i.commit(ctx, Consumed); err != nil {
		return nil, err
	}
	now := readClock(clock)
	if ctx.Err() != nil {
		i.poison()
		return nil, ErrRejected
	}
	authority, err := supervisor.New(grant, now)
	if err != nil {
		i.poison()
		return nil, ErrRejected
	}
	i.authority = authority
	i.binding = grant.Binding
	if ctx.Err() != nil {
		i.poison()
		return nil, ErrRejected
	}
	return authority, nil
}

func readClock(clock func() time.Time) (now time.Time) {
	defer func() {
		if recover() != nil {
			now = time.Time{}
		}
	}()
	return clock()
}

func (i *Issuer) revoke(ctx context.Context) error {
	if i.authority == nil {
		return ErrRejected
	}
	if err := i.authority.Revoke(i.binding); err != nil {
		return i.poison()
	}
	if i.record.Phase == Revoked {
		return nil
	}
	return i.commit(ctx, Revoked)
}

// Revoke はメモリ上の失効を先に行う。記録成功も通信/所有process終了の証明ではない。
func (i *Issuer) Revoke(ctx context.Context) error {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.lease == nil {
		return ErrUnconfigured
	}
	if i.closed || i.poisoned {
		return ErrUnconfirmed
	}
	return i.revoke(ctx)
}

func (i *Issuer) Close(ctx context.Context) error {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.lease == nil {
		return ErrUnconfigured
	}
	if i.poisoned {
		return ErrUnconfirmed
	}
	if i.closed {
		return nil
	}
	if i.authority != nil {
		if err := i.revoke(ctx); err != nil {
			return err
		}
	}
	i.closed = true
	if err := i.lease.Release(); err != nil {
		return i.poison()
	}
	return nil
}
