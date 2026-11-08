// Package macagent is the macOS current-user runtime for agentexec. It starts
// one fixed, non-interactive /bin/sh per command as the already logged-in user,
// in a new session and process group without a controlling terminal, with a
// fixed minimal environment and a cwd chosen by the local composition owner.
// Command bytes reach only the shell's stdin. There is no PTY or interactive
// shell compatibility.
//
// This is not guest isolation: commands can use every resource of the current
// user, and a descendant can leave the process group (for example via setsid).
// The cwd is not a filesystem sandbox. Cleanup proves only the owned shell, the
// group TERM/KILL sent before its single reap, and EOF of its stdio; see
// docs/mac-agent-runtime.md for the trusted composition invariants. There is no
// elevation, service, daemon or TCC use.
package macagent

import (
	"context"
	"errors"
	"io"
	"path"
	"strings"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentexec"
	"github.com/O2-CONNECTIVE/rescue-host/backend/supervisor"
)

const shellPath = "/bin/sh"

// shellArgv is fixed: -s reads commands from stdin; nothing from a request is
// ever placed in argv.
func shellArgv() []string { return []string{shellPath, "-s"} }

// environment is the complete child environment. Nothing is inherited, so host
// signer, key, token or control pipe variables and shell startup hooks such as
// ENV or BASH_ENV never reach the command.
func environment(home string) []string {
	return []string{"HOME=" + home, "PATH=/usr/bin:/bin:/usr/sbin:/sbin", "SHELL=" + shellPath, "LANG=C"}
}

var (
	ErrUnsupported = errors.New("macagent: the current-user runtime is only available on darwin")
	ErrConfig      = errors.New("macagent: runtime composition is invalid")
	// ErrHostState reports a host process state that would break the runtime's
	// guarantees: an inheritable descriptor >= 3, an unreadable descriptor
	// table, or SIGCHLD ignored (which lets the kernel reap the leader).
	ErrHostState = errors.New("macagent: host process state is unsafe for spawning")
	errNoProcess = errors.New("macagent: spawn returned no process")
)

// hostChecks are read-only checks of the host process. They neither close,
// change nor sweep descriptors or signal dispositions; they refuse to spawn.
// They are not atomic with the spawn: the composition invariant in
// docs/mac-agent-runtime.md covers the window between check and fork.
type hostChecks struct {
	inheritableFDs func() error // nil when every descriptor >= 3 is close-on-exec
	childReaping   func() error // nil when SIGCHLD is not ignored
}

func (h hostChecks) run() error {
	if err := h.childReaping(); err != nil {
		return errors.Join(ErrHostState, err)
	}
	if err := h.inheritableFDs(); err != nil {
		return errors.Join(ErrHostState, err)
	}
	return nil
}

const (
	defaultGrace = 2 * time.Second
	defaultPoll  = 10 * time.Millisecond
)

// Config is chosen by the trusted local composition owner (the binary the
// recipient launched), never by a remote request or MCP. agentexec's
// CleanupTimeout must exceed TermGrace+KillWait+IOWait for a first-attempt
// confirmation; a shorter budget leaves cleanup unknown for a retry.
type Config struct {
	TerminalDisposition *TerminalDisposition // trusted local foreground owner only
	WorkDir             string               // absolute cwd for every command; not a sandbox
	Home                string               // the current user's home, exported as HOME
	TermGrace           time.Duration        // TERM to KILL; default 2s
	KillWait            time.Duration        // KILL to confirmed reap; default 2s
	IOWait              time.Duration        // reap to stdio EOF; default 2s
	StartDiagnostics    *StartDiagnostics    // optional fixed local refusal record
	CleanupDiagnostics  *CleanupDiagnostics  // optional fixed local cleanup failure history
}

type spawnSpec struct {
	path string
	argv []string
	env  []string
	dir  string
}

type spawned struct {
	proc   osProcess
	stdin  io.WriteCloser
	stdout io.ReadCloser
	stderr io.ReadCloser
}

// spawnFunc returns proc == nil only when no child process exists.
type spawnFunc func(spawnSpec) (spawned, error)

// Runtime implements agentexec.ProfiledRuntime for ProfileMacCurrentUser.
type Runtime struct {
	terminalDisposition *TerminalDisposition
	spec                spawnSpec
	home                string
	timing              timing
	spawn               spawnFunc
	checks              hostChecks
	diagnostics         *StartDiagnostics
	cleanupDiagnostics  *CleanupDiagnostics
}

var _ agentexec.ProfiledRuntime = (*Runtime)(nil)
var _ agentexec.GuestProcess = (*process)(nil)

// New validates the local composition, refuses a host where SIGCHLD is ignored,
// and selects the darwin adapter. It does not start anything. On other
// platforms it returns ErrUnsupported.
func New(cfg Config) (*Runtime, error) {
	if !nativeSupported {
		return nil, ErrUnsupported
	}
	return newRuntime(cfg, nativeSpawn, nativeChecks, defaultPoll)
}

func newRuntime(cfg Config, spawn spawnFunc, checks hostChecks, poll time.Duration) (*Runtime, error) {
	if spawn == nil || checks.inheritableFDs == nil || checks.childReaping == nil || poll <= 0 || !trustedDir(cfg.WorkDir) || !trustedDir(cfg.Home) {
		return nil, ErrConfig
	}
	if err := checks.childReaping(); err != nil {
		return nil, errors.Join(ErrHostState, err)
	}
	t := timing{termGrace: cfg.TermGrace, killWait: cfg.KillWait, ioWait: cfg.IOWait, poll: poll}
	for _, d := range []*time.Duration{&t.termGrace, &t.killWait, &t.ioWait} {
		switch {
		case *d < 0:
			return nil, ErrConfig
		case *d == 0:
			*d = defaultGrace
		}
	}
	return &Runtime{terminalDisposition: cfg.TerminalDisposition, spec: spawnSpec{path: shellPath, dir: cfg.WorkDir}, home: cfg.Home, timing: t, spawn: spawn, checks: checks, diagnostics: cfg.StartDiagnostics, cleanupDiagnostics: cfg.CleanupDiagnostics}, nil
}

// trustedDir accepts only clean absolute slash paths other than the root.
func trustedDir(p string) bool {
	return p != "" && p != "/" && path.IsAbs(p) && path.Clean(p) == p && !strings.ContainsAny(p, "\x00\n")
}

func (r *Runtime) AgentProfile() agentexec.Profile { return agentexec.ProfileMacCurrentUser }

// StartShell spawns the fixed shell for one command. A process returned with
// an error is still owned by the caller; no process means not started.
func (r *Runtime) StartShell(ctx context.Context, grant supervisor.Binding, stdin []byte) (agentexec.GuestProcess, error) {
	if r == nil || r.spawn == nil {
		if r != nil {
			r.diagnostics.record(StartFailureConfiguration)
		}
		return nil, errors.Join(agentexec.ErrNotStarted, ErrConfig)
	}
	if ctx == nil || ctx.Err() != nil {
		r.diagnostics.record(StartFailureContext)
		return nil, agentexec.ErrNotStarted
	}
	if grant.Generation == 0 {
		r.diagnostics.record(StartFailureBinding)
		return nil, agentexec.ErrNotStarted
	}
	if r.terminalDisposition.IsTerminal() {
		return nil, errors.Join(agentexec.ErrNotStarted, ErrTerminalCleanupUnknown)
	}
	// Re-check immediately before every spawn: a descriptor or SIGCHLD change
	// after construction refuses the command instead of leaking or losing it.
	if err := r.checks.childReaping(); err != nil {
		r.diagnostics.record(startFaultKind(err, StartFailureSIGCHLD))
		return nil, errors.Join(agentexec.ErrNotStarted, ErrHostState, err)
	}
	if err := r.checks.inheritableFDs(); err != nil {
		r.diagnostics.record(startFaultKind(err, StartFailureDescriptorTable))
		return nil, errors.Join(agentexec.ErrNotStarted, ErrHostState, err)
	}
	spec := r.spec
	spec.argv, spec.env = shellArgv(), environment(r.home)
	// Host checks can outlast the original command scope. Re-observe it at
	// spawn admission rather than start new work after an already known stop.
	// This is not atomic with fork: a later stop still owns any late process.
	if ctx.Err() != nil {
		r.diagnostics.record(StartFailureContext)
		return nil, agentexec.ErrNotStarted
	}
	sp, err := r.spawn(spec)
	if sp.proc == nil {
		closeAll(sp)
		if err == nil {
			err = errNoProcess
		}
		r.diagnostics.record(startFaultKind(err, StartFailureSpawnOther))
		return nil, errors.Join(agentexec.ErrNotStarted, err)
	}
	// A process without complete plumbing is still owned and stopped; the
	// placeholders reach EOF, but original pipe ownership and close evidence
	// remain retained through Release.
	var partialPipes [3]*ownedCloser
	if sp.stdin == nil || sp.stdout == nil || sp.stderr == nil {
		var closeErr error
		partialPipes, closeErr = retainPartialPipes(sp)
		err = errors.Join(err, closeErr)
		sp.stdin, sp.stdout, sp.stderr = nopWriteCloser{}, io.NopCloser(eofReader{}), io.NopCloser(eofReader{})
		stdin = nil // Incomplete plumbing is a failed start, not command delivery.
		if err == nil {
			err = errNoProcess
		}
	}
	p := newProcess(sp, r.timing, stdin)
	p.partialPipes = partialPipes        // assigned before ownership is published
	p.diagnostics = r.cleanupDiagnostics // before ownership is returned to any caller
	p.disposition = r.terminalDisposition
	r.terminalDisposition.retain(p)
	return p, err
}

func closeAll(sp spawned) {
	for _, c := range []io.Closer{sp.stdin, sp.stdout, sp.stderr} {
		if c != nil {
			_ = c.Close()
		}
	}
}

type nopWriteCloser struct{}

func (nopWriteCloser) Write(b []byte) (int, error) { return 0, io.ErrClosedPipe }
func (nopWriteCloser) Close() error                { return nil }

type eofReader struct{}

func (eofReader) Read([]byte) (int, error) { return 0, io.EOF }

// retainPartialPipes closes only these supplied streams and retains every
// ownedCloser, including its exact close evidence. Uncertain closes are never
// attempted again; only explicit errCloseRetry permits a later same-owner retry.
func retainPartialPipes(sp spawned) ([3]*ownedCloser, error) {
	var owned [3]*ownedCloser
	var errs []error
	for i, c := range []io.Closer{sp.stdin, sp.stdout, sp.stderr} {
		if c != nil {
			owned[i] = newOwnedCloser(c)
			if err := owned[i].close(); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return owned, errors.Join(errs...)
}
