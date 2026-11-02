// Package childjob は保護所有者が固定したWindows子とJobの所有を扱う。
// Jobは権限隔離ではない。token/保護root/ACLの承認・取得を提供しない。
// 入力をUI、argv、環境変数、保存stateから直接構築してはならない。
package childjob

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/bootstrap"
)

var ErrConfig = errors.New("子の保護起動設定が未設定です")
var ErrUnsupported = errors.New("Windowsの保護子起動を利用できません")
var ErrStart = errors.New("子の起動を確認できません")
var ErrUnconfirmed = errors.New("子とJobの終了を確認できません")
var ErrExit = errors.New("子は異常終了しました")

type Artifact struct {
	Path   string
	SHA256 [32]byte
}

// Handlesは保護所有者が既に所有した標準pipeだけを借用する。
// このmoduleは元handleやprimary tokenの所有を引き取らない。
type Handles struct{ Stdin, Stdout, Stderr uintptr }

// Bootstrap は同じ保護所有者が準備した第4read handleと非秘密の相関値です。
// この値やlocatorの一致は認証ではなく、childのnative factoryが別に照合します。
type Bootstrap struct {
	ReadHandle uintptr
	Values     bootstrap.LaunchValues
}

// Specは未決の保護provisioningからの型付き入力で、認証の証明ではない。
// PrimaryTokenは承認済み既存primary tokenが必須。取得や昇格fallbackはしない。
type Spec struct {
	Artifact       Artifact
	Handles        Handles
	PrimaryToken   uintptr
	CleanupTimeout time.Duration
	Bootstrap      *Bootstrap
	Command        *FixedCommand
	Protection     *ProtectedGuestConfig
}

type kernel interface {
	verifyArtifact(context.Context, Artifact) (uintptr, error)
	newJob() (uintptr, error)
	createSuspended(Spec) (uintptr, uintptr, error)
	assign(uintptr, uintptr) error
	resume(uintptr) error
	terminateJob(uintptr) error
	terminateProcess(uintptr) error
	waitProcess(context.Context, uintptr) (uint32, error)
	waitEmpty(context.Context, uintptr) error
	close(uintptr) error
}

// Childはprocess/Jobの終了確認までhandleを保持する。初回利用後はコピーしない。
// 元pipe、primary token、PIDの所有を引き取らず、PID検索による再取得もしない。
type Child struct {
	api                         kernel
	gate                        chan struct{}
	image, job, process, thread uintptr
	dependencies                []uintptr
	assigned, exited            bool
	exitCode                    uint32
}

func bounded(ctx context.Context) bool {
	if ctx == nil || ctx.Err() != nil {
		return false
	}
	deadline, ok := ctx.Deadline()
	return ok && time.Now().Before(deadline)
}

func configured(spec Spec) bool {
	if spec.Protection != nil && !spec.Protection.valid {
		return false
	}
	h := spec.Handles
	if _, err := launchArgs(spec); err != nil {
		return false
	}
	return spec.Artifact.Path != "" && !strings.ContainsRune(spec.Artifact.Path, 0) && spec.Artifact.SHA256 != ([32]byte{}) &&
		ordinaryHandle(spec.PrimaryToken) && spec.CleanupTimeout > 0 &&
		ordinaryHandle(h.Stdin) && ordinaryHandle(h.Stdout) && ordinaryHandle(h.Stderr) &&
		h.Stdin != h.Stdout && h.Stdin != h.Stderr && h.Stdout != h.Stderr
}

// INVALID_HANDLE_VALUEと現在process/thread/token等の負のpseudo handleを採らない。
func ordinaryHandle(handle uintptr) bool { return handle != 0 && handle < ^uintptr(15) }

func Start(ctx context.Context, spec Spec) (*Child, error) {
	spec = freezeSpec(spec)
	if !bounded(ctx) || !configured(spec) || spec.Protection != nil {
		return nil, ErrConfig
	}
	return platformStart(ctx, spec)
}

func start(ctx context.Context, spec Spec, api kernel) (*Child, error) {
	if spec.Protection != nil {
		return nil, ErrConfig
	}
	c, err := stage(ctx, spec, api)
	if err != nil {
		return c, err
	}
	if err := c.resumeOwned(ctx); err != nil {
		cleanup, cancel := context.WithTimeout(context.Background(), spec.CleanupTimeout)
		defer cancel()
		if c.Close(cleanup) != nil {
			return c, ErrUnconfirmed
		}
		return nil, ErrStart
	}
	return c, nil
}

func (c *Child) resumeOwned(ctx context.Context) error {
	if !bounded(ctx) || c.api.resume(c.thread) != nil || !bounded(ctx) {
		return ErrStart
	}
	if c.release(&c.thread) != nil || c.release(&c.image) != nil || !bounded(ctx) {
		return ErrStart
	}
	return nil
}

func stage(ctx context.Context, spec Spec, api kernel) (*Child, error) {
	spec = freezeSpec(spec)
	if !bounded(ctx) || !configured(spec) || api == nil {
		return nil, ErrConfig
	}
	c := &Child{api: api, gate: make(chan struct{}, 1)}
	c.gate <- struct{}{}
	fail := func() (*Child, error) {
		cleanup, cancel := context.WithTimeout(context.Background(), spec.CleanupTimeout)
		defer cancel()
		if err := c.Close(cleanup); err != nil {
			// 部分起動の所有参照を捨てず、呼出側へ回収責務ごと返す。
			return c, ErrUnconfirmed
		}
		return nil, ErrStart
	}
	var err error
	c.image, err = api.verifyArtifact(ctx, spec.Artifact)
	if err != nil || c.image == 0 || ctx.Err() != nil {
		return fail()
	}
	if spec.Command != nil {
		for _, dependency := range spec.Command.dependencies {
			handle, verifyErr := api.verifyArtifact(ctx, dependency)
			// 検証途中の失敗でも返された同じhandleを回収責務ごと保持します。
			c.dependencies = append(c.dependencies, handle)
			if verifyErr != nil || handle == 0 || ctx.Err() != nil {
				return fail()
			}
		}
	}
	if spec.Protection != nil {
		protected, ok := api.(protectedGuestKernel)
		if !ok || inspectSecurity(protected, *spec.Protection, spec.PrimaryToken, protectedToken) != nil {
			return fail()
		}
		c.job, err = protected.newProtectedJob(*spec.Protection)
	} else {
		c.job, err = api.newJob()
	}
	if err != nil || c.job == 0 || ctx.Err() != nil {
		return fail()
	}
	c.process, c.thread, err = api.createSuspended(spec)
	if err != nil || c.process == 0 || c.thread == 0 || ctx.Err() != nil {
		return fail()
	}
	if spec.Protection != nil {
		protected := api.(protectedGuestKernel)
		for _, target := range []struct {
			h    uintptr
			kind protectedKind
		}{{c.job, protectedJob}, {c.process, protectedProcess}, {c.thread, protectedThread}} {
			if inspectSecurity(protected, *spec.Protection, target.h, target.kind) != nil || !bounded(ctx) {
				return fail()
			}
		}
	}
	if err = api.assign(c.job, c.process); err != nil || ctx.Err() != nil {
		return fail()
	}
	c.assigned = true
	return c, nil
}

func freezeSpec(spec Spec) Spec {
	if spec.Protection != nil {
		snapshot := *spec.Protection
		spec.Protection = &snapshot
	}
	spec.Command = copyCommand(spec.Command)
	if spec.Bootstrap != nil {
		snapshot := *spec.Bootstrap
		spec.Bootstrap = &snapshot
	}
	return spec
}

func (c *Child) take(ctx context.Context) error {
	if c == nil || !bounded(ctx) {
		return ErrConfig
	}
	select {
	case <-ctx.Done():
		return ErrUnconfirmed
	case <-c.gate:
	}
	if ctx.Err() != nil {
		c.gate <- struct{}{}
		return ErrUnconfirmed
	}
	return nil
}

func (c *Child) release(handle *uintptr) error {
	if *handle == 0 {
		return nil
	}
	if err := c.api.close(*handle); err != nil {
		return ErrUnconfirmed
	}
	*handle = 0
	return nil
}

// WaitExit observes only this held leader. A successful code is not proof of
// natural exit, execution admission, Job emptiness, pipe EOF or handle cleanup.
// It shares the original owner gate/cache with Wait and Close, and never closes
// handles, terminates work or grants admission. The code is valid only on nil error.
func (c *Child) WaitExit(ctx context.Context) (uint32, error) {
	if c == nil || c.api == nil || c.gate == nil {
		return 0, ErrConfig
	}
	if err := c.take(ctx); err != nil {
		if ctx != nil {
			return 0, errors.Join(err, ctx.Err())
		}
		return 0, err
	}
	defer func() { c.gate <- struct{}{} }()
	code, err := c.observeExitOwned(ctx)
	if err != nil {
		return 0, errors.Join(err, ctx.Err())
	}
	if err := ctx.Err(); err != nil {
		// Retain a real successful OS observation, but do not report success
		// for a call whose budget expired while that observation returned.
		return 0, errors.Join(ErrUnconfirmed, err)
	}
	return code, nil
}

// The caller holds c.gate. Failed observations never set exited or cache a code.
// Original OS errors remain available to the internal caller, not public UI.
func (c *Child) observeExitOwned(ctx context.Context) (uint32, error) {
	if c.exited {
		return c.exitCode, nil
	}
	if c.process == 0 {
		return 0, ErrUnconfirmed
	}
	code, err := c.api.waitProcess(ctx, c.process)
	if err != nil {
		return 0, errors.Join(ErrUnconfirmed, err)
	}
	c.exitCode, c.exited = code, true
	return code, nil
}

func (c *Child) waitOwned(ctx context.Context) error {
	if c.process != 0 && !c.exited {
		if _, err := c.observeExitOwned(ctx); err != nil {
			return ErrUnconfirmed
		}
	}
	if c.job != 0 {
		if err := c.api.waitEmpty(ctx, c.job); err != nil {
			return ErrUnconfirmed
		}
	}
	if ctx.Err() != nil {
		return ErrUnconfirmed
	}
	var unconfirmed bool
	for _, handle := range []*uintptr{&c.thread, &c.image} {
		if err := c.release(handle); err != nil {
			unconfirmed = true
		}
	}
	// module importやleader退出だけでは解放しません。JobEmptyまで同じ参照です。
	for i := range c.dependencies {
		if err := c.release(&c.dependencies[i]); err != nil {
			unconfirmed = true
		}
	}
	for _, handle := range []*uintptr{&c.process, &c.job} {
		if err := c.release(handle); err != nil {
			unconfirmed = true
		}
	}
	if unconfirmed || ctx.Err() != nil {
		return ErrUnconfirmed
	}
	return nil
}

// Waitはprocessの退出とJob active=0、所有handleの回収まで確認する。
// 成功はGoのClose応答やSSHの停止証明ではない。非0退出はErrExitと区別する。
func (c *Child) Wait(ctx context.Context) error {
	if err := c.take(ctx); err != nil {
		return err
	}
	defer func() { c.gate <- struct{}{} }()
	if err := c.waitOwned(ctx); err != nil {
		return err
	}
	if c.exited && c.exitCode != 0 {
		return ErrExit
	}
	return nil
}

// Closeは明示停止後もJobを空と確認するまで最後のhandleを保持する。
// 協調的なOS API呼出しが前提で、OS hang/sleep中の強制執行を保証しない。
func (c *Child) Close(ctx context.Context) error {
	if err := c.take(ctx); err != nil {
		return err
	}
	defer func() { c.gate <- struct{}{} }()
	// leader退出後も子孫が残り得る。Job参照があれば常にJob全体の停止を要求する。
	if c.job != 0 {
		_ = c.api.terminateJob(c.job)
	}
	if c.process != 0 && !c.exited {
		// 割当失敗の結果が未知でも、停止中のprocess本体を取り残さない。
		if !c.assigned {
			_ = c.api.terminateProcess(c.process)
		}
	}
	return c.waitOwned(ctx)
}
