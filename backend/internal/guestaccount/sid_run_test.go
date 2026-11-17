package guestaccount

import (
	"bytes"
	"context"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/childjob"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/childstdio"
)

type sidRunInputFixture struct {
	mu   sync.Mutex
	data []byte
	eof  chan struct{}
	once sync.Once
}

func (w *sidRunInputFixture) Write(b []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.data = append(w.data, b...)
	return len(b), nil
}
func (w *sidRunInputFixture) Close() error { w.once.Do(func() { close(w.eof) }); return nil }

type sidRunPipesFixture struct {
	input                *sidRunInputFixture
	output, stderr       io.ReadCloser
	mu                   sync.Mutex
	released, closed     int
	releaseErr, closeErr error
	inputOverride        io.WriteCloser
	closeHook            func(context.Context) error
}

func (p *sidRunPipesFixture) ChildHandles() childjob.Handles {
	return childjob.Handles{Stdin: 31, Stdout: 32, Stderr: 33}
}
func (p *sidRunPipesFixture) Input() io.WriteCloser {
	if p.inputOverride != nil {
		return p.inputOverride
	}
	return p.input
}
func (p *sidRunPipesFixture) Output() io.ReadCloser { return p.output }
func (p *sidRunPipesFixture) Stderr() io.ReadCloser { return p.stderr }
func (p *sidRunPipesFixture) ReleaseChild(context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.released++
	return p.releaseErr
}
func (p *sidRunPipesFixture) Close(ctx context.Context) error {
	p.mu.Lock()
	p.closed++
	e, hook := p.closeErr, p.closeHook
	p.mu.Unlock()
	if hook != nil {
		return hook(ctx)
	}
	return e
}

type sidRunChildFixture struct {
	input             *sidRunInputFixture
	stop              chan struct{}
	once              sync.Once
	mu                sync.Mutex
	waited, closed    int
	waitErr, closeErr error
}

func (c *sidRunChildFixture) Wait(ctx context.Context) error {
	select {
	case <-c.input.eof:
	case <-c.stop:
	case <-ctx.Done():
		return ctx.Err()
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.waited++
	return c.waitErr
}
func (c *sidRunChildFixture) Close(context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed++
	if c.closeErr == nil {
		c.once.Do(func() { close(c.stop) })
	}
	return c.closeErr
}

func sidRunFixture(t *testing.T) (*sidRunOwner, *sidRunPipesFixture, *sidRunChildFixture) {
	t.Helper()
	command, e := childjob.NewFixedCommand([]string{"-Fixed"}, nil)
	if e != nil {
		t.Fatal(e)
	}
	config := sidRunConfig{spec: childjob.Spec{Artifact: childjob.Artifact{Path: `C:\Protected\powershell.exe`, SHA256: [32]byte{1}}, PrimaryToken: 20, Command: command, CleanupTimeout: 50 * time.Millisecond}, stdio: childstdio.Setup{UserSID: "S-1-5-18", ServiceSID: "S-1-5-80-1-2-3-4-5", CleanupTimeout: 50 * time.Millisecond}}
	p := &sidRunPipesFixture{input: &sidRunInputFixture{eof: make(chan struct{})}, output: io.NopCloser(bytes.NewReader([]byte("canonical-result\n"))), stderr: io.NopCloser(bytes.NewReader(nil))}
	c := &sidRunChildFixture{input: p.input, stop: make(chan struct{})}
	o, e := newSIDRunOwner(config, sidRunOps{open: func(context.Context, childstdio.Setup) (sidRunPipes, error) { return p, nil }, start: func(_ context.Context, s childjob.Spec) (sidRunChild, error) {
		if s.Handles != p.ChildHandles() || s.Command != command || s.Bootstrap != nil {
			t.Error("fixed command or exact child handles replaced")
		}
		return c, nil
	}})
	if e != nil {
		t.Fatal(e)
	}
	return o, p, c
}

// sidRunConfirmBudget は、清掃の成否と理由を確かめる Close（execute の延期 Close を含む）の予算を、fixture の 50ms から
// testContext と同じ 1 秒に延ばす。join は done を受けた時点で戻るので成功時の所要は変わらず、清掃が待つ goroutine が
// 2 コアの runner で遅れて動いても、期限切れの ErrCleanup と取り違えない。期限切れを確かめる検査がある試験では、その後で呼ぶ。
func sidRunConfirmBudget(o *sidRunOwner) { o.config.spec.CleanupTimeout = time.Second }

func TestSIDRunReturnsOnlyAfterEOFWaitAndPipeCleanup(t *testing.T) {
	o, p, c := sidRunFixture(t)
	request := []byte("canonical-request\n")
	out, e := o.execute(testContext(t), request)
	if e != nil || !bytes.Equal(out, []byte("canonical-result\n")) {
		t.Fatal("completed exchange did not return exact stdout", string(out), e)
	}
	if !bytes.Equal(p.input.data, request) || p.released != 1 || p.closed != 1 || c.waited != 1 || c.closed != 0 {
		t.Fatal("output returned before full request/EOF/job/pipe cleanup", p.released, p.closed, c.waited, c.closed)
	}
	if e = o.Close(testContext(t)); e != nil {
		t.Fatal(e)
	}
	if out, e = o.execute(testContext(t), request); e == nil || out != nil {
		t.Fatal("owner executed twice")
	}
}
