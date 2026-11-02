package serviceprovision

import (
	"context"
	"errors"
	"sync/atomic"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/bootstrapauth"
	"github.com/O2-CONNECTIVE/rescue-host/backend/servicehost"
)

// private scope seam。製品では既存bootstrapauth.PreparedServiceだけを保持する。
type preparedServiceScope interface {
	Recheck(context.Context) error
	Close(context.Context) error
}

// Prepared は固定rootとservice imageの同じ元threadでの保持証拠を所有する。
// 設定の読取成功を、launch tokenやoperator承認へ変換しない。
type Prepared struct {
	loaded   *Loaded
	scope    preparedServiceScope
	record   Record
	accepted atomic.Bool
	poisoned atomic.Bool
}

// PrepareFixed はcallerが固定した元OS threadだけで使用し、partial ownerも返す。
// この内部APIへUI/env/argvのpathを渡す経路は設けない。
func PrepareFixed(ctx context.Context, anchor Anchor) (*Prepared, error) {
	root, err := NewNativeRoot(anchor)
	if err != nil {
		return nil, err
	}
	return root.PrepareService(ctx)
}

// PrepareServiceは同じ固定rootをservice imageの準備検査へ接続する。
// callerはOpenとCloseを含む全期間で元OS threadを保持する。
func (root *Root) PrepareService(ctx context.Context) (*Prepared, error) {
	loaded, err := root.Open(ctx)
	if loaded == nil {
		if err == nil {
			err = ErrRootDenied
		}
		return nil, err
	}
	p := &Prepared{loaded: loaded}
	if err != nil {
		return p, p.reject(err)
	}
	data, err := loaded.Record()
	if err != nil {
		return p, p.reject(err)
	}
	p.record, err = DecodeRecord(data)
	clear(data)
	if err != nil {
		return p, p.reject(err)
	}
	subject, err := loaded.Subject()
	if err != nil {
		return p, p.reject(err)
	}
	if subject.ServiceName != p.record.Profile.ServiceName || subject.ServiceSID != p.record.Profile.Keys.ServiceSID || subject.ProcessUserSID != p.record.Profile.Keys.ProcessUserSID {
		return p, ErrRecord
	}
	token, err := loaded.QueryToken()
	if err != nil {
		return p, p.reject(err)
	}
	factory, err := bootstrapauth.NewNativeFactory(p.record.Profile)
	if err != nil {
		return p, p.reject(err)
	}
	p.scope, err = factory.PrepareService(ctx, token)
	if err != nil {
		return p, p.reject(err)
	}
	if !rootLive(ctx) {
		return p, ErrRootDenied
	}
	p.accepted.Store(true)
	return p, nil
}

// ComposeServiceは保持中の同じrecordを、既存Serviceの構成へ渡す。
// tokenはquery-onlyから昇格せず、別の保護供給元が全回収まで所有する。
// 成功は起動handleの権限やoperator承認の認証を証明せず、OS起動も行わない。
func (p *Prepared) ComposeService(ctx context.Context, token uintptr, approval servicehost.ProtectedProvisioning) (*servicehost.Service, error) {
	if p != nil && p.poisoned.Load() {
		return nil, ErrRootFatal
	}
	if p == nil || !p.accepted.Load() || p.loaded == nil || p.scope == nil || !rootLive(ctx) {
		return nil, ErrRootDenied
	}
	if _, err := p.loaded.QueryToken(); err != nil {
		return nil, p.reject(err)
	}
	if err := p.scope.Recheck(ctx); err != nil {
		return nil, p.reject(err)
	}
	if !rootLive(ctx) || !p.accepted.Load() {
		return nil, ErrRootDenied
	}
	return ComposeService(p.record, token, approval)
}

func (p *Prepared) Close(ctx context.Context) error {
	if p == nil {
		return nil
	}
	p.accepted.Store(false)
	var result error
	if p.scope != nil {
		result = p.scope.Close(ctx)
	}
	if p.loaded != nil {
		result = errors.Join(result, p.loaded.Close(ctx))
	}
	return p.reject(result)
}

func (p *Prepared) reject(err error) error {
	p.accepted.Store(false)
	if errors.Is(err, ErrRootFatal) {
		p.poisoned.Store(true)
	}
	if p.poisoned.Load() {
		return errors.Join(ErrRootFatal, err)
	}
	return err
}

// 固定評価entryの配置値と予算は未設定。対象をレビューしてsourceに固定する。
// 環境変数、argv、UI、実行pathから補完する入口は設けない。
func fixedEvaluationConfig() PreviewExecutionConfig {
	return PreviewExecutionConfig{Anchor: fixedInstallationAnchor()}
}

func newFixedExecution(c PreviewExecutionConfig) (*Execution, error) {
	e, err := NewNativeRuntimeExecution(c)
	if err != nil {
		return nil, err
	}
	e.fixedEvaluation = true
	return e, nil
}

// RunFixedは固定評価sourceによるruntime ownerとSCM全回収を結ぶ専用入口。
// 既定は全値未設定でnative APIより前に拒否する。実行許可や設定を補完しない。
func RunFixed(ctx context.Context) error {
	if ctx == nil || ctx.Err() != nil {
		return ErrApprovalUnconfigured
	}
	execution, err := newFixedExecution(fixedEvaluationConfig())
	if err != nil {
		return err
	}
	return execution.Run(ctx)
}
