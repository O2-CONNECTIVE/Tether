//go:build darwin && cgo && ci_actual_producer

package residentpipe

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"
)

type ciChild struct {
	diagnosticCode                 string
	lastSource                     uint64
	cmd                            *exec.Cmd
	input, output, diagnostic, log *os.File
	launch                         []*os.File
	started                        bool
	waitDone, readDone             chan struct{}
	mu                             sync.Mutex
	waitErr, readErr               error
	snapshot                       Snapshot
	observed                       time.Time
	receipts                       map[uint64]Receipt
	closed                         bool
	unknown                        bool
	nextID                         uint64
}

func startCIChild(binary, root string, b ciBootstrap) (*ciChild, error) {
	p := &ciChild{waitDone: make(chan struct{}), readDone: make(chan struct{}), receipts: make(map[uint64]Receipt)}
	childIn, input, err := os.Pipe()
	if err != nil {
		return p, err
	}
	p.input = input
	p.launch = append(p.launch, childIn)
	output, childOut, err := os.Pipe()
	if err != nil {
		return p, err
	}
	p.output = output
	p.launch = append(p.launch, childOut)
	bootstrap, sendBootstrap, err := os.Pipe()
	if err != nil {
		return p, err
	}
	p.launch = append(p.launch, bootstrap, sendBootstrap)
	p.diagnostic, err = os.OpenFile(filepath.Join(root, "child.stderr"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return p, err
	}
	p.log, err = os.OpenFile(filepath.Join(root, "child.stdout"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return p, err
	}
	p.cmd = exec.Command(binary)
	p.cmd.Env = []string{"PATH=/usr/bin:/bin:/usr/sbin:/sbin", "LANG=C"}
	p.cmd.Dir = root
	p.cmd.Stdin = childIn
	p.cmd.Stdout = childOut
	p.cmd.Stderr = p.diagnostic
	p.cmd.ExtraFiles = []*os.File{bootstrap}
	if err = p.cmd.Start(); err != nil {
		return p, err
	}
	p.started = true
	go func() { e := p.cmd.Wait(); p.mu.Lock(); p.waitErr = e; p.mu.Unlock(); close(p.waitDone) }()
	go p.read()
	// Retire inherited duplicates before bootstrap/ON; no descriptor repairs.
	for _, f := range []*os.File{childIn, childOut, bootstrap} {
		if err = f.Close(); err != nil {
			return p, err
		}
	}
	p.launch = []*os.File{sendBootstrap}
	raw, err := json.Marshal(b)
	if err != nil {
		return p, err
	}
	defer clear(raw)
	if len(raw) > 16384 {
		return p, errors.New("bootstrap exceeds bound")
	}
	if err = sendBootstrap.SetWriteDeadline(time.Now().Add(5 * time.Second)); err != nil {
		return p, err
	}
	n, err := sendBootstrap.Write(raw)
	if err != nil || n != len(raw) {
		return p, errors.New("bootstrap write unknown")
	}
	if err = sendBootstrap.Close(); err != nil {
		return p, err
	}
	p.launch = nil
	return p, nil
}
func (p *ciChild) read() {
	defer close(p.readDone)
	reader := bufio.NewReaderSize(p.output, MaxLine+1)
	var failure error
	failureCode := "producer-envelope-invalid"
	for {
		raw, err := reader.ReadSlice('\n')
		if len(raw) > 0 {
			n, writeErr := p.log.Write(raw) // exact bytes, including original terminator
			if writeErr != nil || n != len(raw) {
				failureCode = "producer-log-write"
				failure = ErrProtocol
				break
			}
		}
		if err == io.EOF && len(raw) == 0 {
			break
		}
		if err != nil || validateFrame(raw) != nil {
			failureCode = "producer-frame-malformed"
			if err == io.EOF {
				failureCode = "producer-eof-partial"
			} else if errors.Is(err, bufio.ErrBufferFull) {
				failureCode = "producer-frame-oversized"
			} else if err != nil {
				failureCode = "producer-read"
			}
			failure = ErrProtocol
			break
		}
		if err = p.acceptFrame(raw); err != nil {
			failure = err
			break
		}
	}
	if failure != nil {
		p.mu.Lock()
		p.readErr = failure
		if p.diagnosticCode == "" {
			p.diagnosticCode = failureCode
		}
		p.unknown = true
		p.mu.Unlock()
		return
	}
	if err := p.output.Close(); err != nil {
		p.noteDiagnostic("producer-output-close")
		failure = errors.Join(failure, err)
	}
	if err := p.log.Sync(); err != nil {
		p.noteDiagnostic("producer-log-sync")
		failure = errors.Join(failure, err)
	}
	if err := p.log.Close(); err != nil {
		p.noteDiagnostic("producer-log-close")
		failure = errors.Join(failure, err)
	}
	p.mu.Lock()
	p.readErr = failure
	p.mu.Unlock()
}
func ciStrictFrame(raw []byte, v any) error {
	if validateFrame(raw) != nil {
		return ErrProtocol
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	if walk(d) != nil {
		return ErrProtocol
	}
	if _, e := d.Token(); e != io.EOF {
		return ErrProtocol
	}
	d = json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(v) != nil || d.Decode(new(any)) != io.EOF {
		return ErrProtocol
	}
	return nil
}
func (p *ciChild) acceptFrame(raw []byte) error {
	var root map[string]json.RawMessage
	if ciStrictFrame(raw, &root) != nil {
		return ErrProtocol
	}
	var kind string
	if json.Unmarshal(root["type"], &kind) != nil {
		return ErrProtocol
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return ErrProtocol
	}
	switch kind {
	case "status":
		var e statusEnvelope
		if !exactKeys(root, "protocolVersion", "type", "sourceContinuousMs", "snapshot") || ciStrictFrame(raw, &e) != nil || e.ProtocolVersion != 1 || bytes.Equal(bytes.TrimSpace(root["sourceContinuousMs"]), []byte("null")) || e.SourceContinuousMS > MaxInteger || e.SourceContinuousMS < p.lastSource {
			return ErrProtocol
		}
		var body map[string]json.RawMessage
		_ = json.Unmarshal(root["snapshot"], &body)
		if !exactKeys(body, "protocolVersion", "source", "state", "revision", "validForMs", "standby", "registration", "caller", "work", "cleanup", "canRequestOn", "canRequestOff", "transition") {
			return ErrProtocol
		}
		for _, field := range []string{"canRequestOn", "canRequestOff"} {
			v := bytes.TrimSpace(body[field])
			if !bytes.Equal(v, []byte("true")) && !bytes.Equal(v, []byte("false")) {
				return ErrProtocol
			}
		}
		x := e.Snapshot
		if x.ProtocolVersion != 1 || x.Source != "resident-owner" || x.State != "source-observation" || x.Revision <= p.snapshot.Revision || x.Revision > MaxInteger || x.ValidForMS != 3000 || !oneOf(x.Standby, "off", "on", "stopping", "unknown") || !oneOf(x.Registration, "disconnected", "connecting", "authenticated", "unknown") || !oneOf(x.Work, "idle", "preparing", "running", "stopping", "unknown") || !oneOf(x.Cleanup, "confirmed", "pending", "unknown") {
			return ErrProtocol
		}
		if x.Transition != nil {
			var tr map[string]json.RawMessage
			_ = json.Unmarshal(body["transition"], &tr)
			if !exactKeys(tr, "requestId", "intent", "outcome") || x.Transition.RequestID == 0 || x.Transition.RequestID > MaxInteger || !oneOf(x.Transition.Intent, "on", "off") || !oneOf(x.Transition.Outcome, "pending", "applied", "rejected", "unknown") {
				return ErrProtocol
			}
		}
		p.lastSource = e.SourceContinuousMS
		p.snapshot = x
		p.observed = time.Now()
		if x.Cleanup == "unknown" {
			p.unknown = true
			if p.diagnosticCode == "" {
				p.diagnosticCode = "producer-cleanup-unknown"
			}
		} else if x.Transition != nil && x.Transition.Outcome == "unknown" {
			p.unknown = true
			if p.diagnosticCode == "" {
				p.diagnosticCode = "producer-transition-unknown"
			}
		}
	case "receipt":
		var e receiptEnvelope
		if !exactKeys(root, "protocolVersion", "type", "receipt") || ciStrictFrame(raw, &e) != nil || e.ProtocolVersion != 1 || e.Receipt.ProtocolVersion != 1 {
			return ErrProtocol
		}
		var body map[string]json.RawMessage
		_ = json.Unmarshal(root["receipt"], &body)
		if !exactKeys(body, "protocolVersion", "requestId", "intent", "disposition") || e.Receipt.RequestID == 0 || e.Receipt.RequestID > MaxInteger || !oneOf(e.Receipt.Intent, "on", "off") || !oneOf(e.Receipt.Disposition, "accepted", "rejected", "stale", "unknown") {
			return ErrProtocol
		}
		if _, ok := p.receipts[e.Receipt.RequestID]; ok {
			return ErrProtocol
		}
		p.receipts[e.Receipt.RequestID] = e.Receipt
		if e.Receipt.Disposition == "unknown" {
			if p.diagnosticCode == "" {
				p.diagnosticCode = "producer-receipt-unknown"
			}
			p.unknown = true
		}
	case "closed":
		var e closedEnvelope
		if !exactKeys(root, "protocolVersion", "type", "sourceContinuousMs", "outcome") || ciStrictFrame(raw, &e) != nil || e.ProtocolVersion != 1 || bytes.Equal(bytes.TrimSpace(root["sourceContinuousMs"]), []byte("null")) || e.SourceContinuousMS > MaxInteger || e.SourceContinuousMS < p.lastSource || e.Outcome != "confirmed" {
			p.unknown = true
			return ErrProtocol
		}
		p.lastSource = e.SourceContinuousMS
		p.closed = true
	default:
		return ErrProtocol
	}
	return nil
}
func (p *ciChild) state() (Snapshot, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.unknown || p.readErr != nil {
		return p.snapshot, errors.New("original producer observation unknown")
	}
	if p.observed.IsZero() || time.Since(p.observed) > 3*time.Second {
		return p.snapshot, errors.New("no fresh producer snapshot")
	}
	return p.snapshot, nil
}
func (p *ciChild) await(match func(Snapshot) bool, budget time.Duration) error {
	until := time.Now().Add(budget)
	for time.Now().Before(until) {
		p.mu.Lock()
		unknown := p.unknown
		readErr := p.readErr
		p.mu.Unlock()
		if unknown || readErr != nil {
			return errors.New("producer unknown/protocol failure")
		}
		if s, e := p.state(); e == nil && match(s) {
			return nil
		}
		select {
		case <-p.waitDone:
			return errors.New("producer ended before condition")
		default:
		}
		time.Sleep(10 * time.Millisecond)
	}
	return errors.New("producer observation deadline; original owner retained")
}
func (p *ciChild) request(intent string) (uint64, error) {
	s, e := p.state()
	if e != nil {
		return 0, e
	}
	p.nextID++
	id := p.nextID
	raw, e := Encode(intentEnvelope{1, "intent", Intent{1, id, s.Revision, intent}})
	if e != nil {
		return id, e
	}
	if e = p.input.SetWriteDeadline(time.Now().Add(5 * time.Second)); e != nil {
		return id, e
	}
	n, e := p.input.Write(raw)
	if e != nil || n != len(raw) {
		return id, fmt.Errorf("intent write unknown: %w", e)
	}
	return id, nil
}
func (p *ciChild) applied(id uint64, intent string) error {
	if err := p.await(func(s Snapshot) bool {
		return s.Transition != nil && s.Transition.RequestID == id && s.Transition.Intent == intent && s.Transition.Outcome == "applied"
	}, 45*time.Second); err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	r, ok := p.receipts[id]
	if !ok || r.Disposition != "accepted" || r.Intent != intent {
		return errors.New("applied transition missing accepted same-ID receipt")
	}
	return nil
}
func (p *ciChild) finish() error {
	if err := p.input.Close(); err != nil {
		p.noteDiagnostic("producer-input-close")
		return err
	}
	select {
	case <-p.readDone:
	case <-time.After(90 * time.Second):
		return errors.New("original producer EOF not joined")
	}
	select {
	case <-p.waitDone:
	case <-time.After(5 * time.Second):
		return errors.New("original child Wait not joined")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.diagnosticCode == "" {
		if p.waitErr != nil {
			p.diagnosticCode = "producer-wait-nonzero"
		} else if !p.closed {
			p.diagnosticCode = "producer-closed-missing"
		} else if p.readErr != nil {
			p.diagnosticCode = "producer-read"
		} else if p.unknown {
			p.diagnosticCode = "producer-reported-unknown"
		}
	}
	if p.unknown || !p.closed || p.readErr != nil || p.waitErr != nil {
		return errors.Join(errors.New("original producer cleanup not confirmed"), p.readErr, p.waitErr)
	}
	err := p.diagnostic.Close()
	if err != nil && p.diagnosticCode == "" {
		p.diagnosticCode = "producer-diagnostic-close"
	}
	return err
}

func (p *ciChild) noteDiagnostic(code string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.diagnosticCode == "" {
		p.diagnosticCode = code
	}
}
