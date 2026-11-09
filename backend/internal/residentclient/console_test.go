package residentclient

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
)

type memoryConsole struct { input []string; output [][]byte; reads int; writeFailure bool; partial bool }
func (m *memoryConsole) ReadLine(context.Context) ([]byte, error) {
	m.reads++
	if m.partial {return []byte("start"),io.EOF}
	if len(m.input) == 0 { return nil, io.EOF }
	s := m.input[0]; m.input = m.input[1:]; return []byte(s), nil
}
func (m *memoryConsole) WriteLine(_ context.Context, b []byte) error {
	m.output = append(m.output, append([]byte(nil), b...))
	if m.writeFailure{return io.ErrShortWrite};return nil
}

func TestConsoleEOFPartialFrameAndOutputUncertaintyCloseOnce(t *testing.T) {
	for _,w:=range []*memoryConsole{{},{partial:true},{writeFailure:true}} {
		s:=&memorySession{}
		e:=Serve(context.Background(),w,s)
		if s.closes!=1 || len(s.operations)!=0 {t.Fatal("close ownership lost")}
		if (w.partial || w.writeFailure)!=(e!=nil) {t.Fatal("uncertainty not retained")}
		if w.writeFailure && len(w.output)!=1 {t.Fatal("partial output retried")}
	}
}

func TestConsoleCanceledContextCannotStartWork(t *testing.T) {
	ctx,cancel:=context.WithCancel(context.Background());cancel()
	w:=&memoryConsole{input:[]string{"start "+strings.Repeat("a",32)+"\n"}};s:=&memorySession{}
	if Serve(ctx,w,s)==nil || s.closes!=1 || w.reads!=0 || len(s.operations)!=0 {t.Fatal("canceled session started work")}
}
type memorySession struct { operations []string; closes int; failure error }
func (m *memorySession) Invoke(_ context.Context, op, id string) (json.RawMessage, error) {
	m.operations = append(m.operations, op+" "+id)
	return json.RawMessage(`{"error":"not_found"}`), m.failure
}
func (m *memorySession) Close(context.Context) error { m.closes++; return m.failure }

func TestConsoleKeepsOneSessionAndCloseIsNotCancel(t *testing.T) {
	id := strings.Repeat("a",32)
	w := &memoryConsole{input: []string{"result "+id+"\n", "cancel "+id+"\n", "close\n", "start "+id+"\n"}}
	s := &memorySession{}
	if Serve(context.Background(), w, s) != nil || s.closes != 1 || len(s.operations) != 2 || w.reads != 3 { t.Fatal("session or close semantics changed") }
	if !strings.Contains(string(w.output[len(w.output)-1]), `"work_cleanup":"not_observed"`) { t.Fatal("close became work proof") }
}
func TestConsoleRejectsInvalidFramesAndStillClosesOriginalSession(t *testing.T) {
	for _, line := range []string{"start\n", "result abc\n", "close\r\n", " close\n", "close", "start "+strings.Repeat("A",32)+"\n", strings.Repeat("x",MaxInput)+"\n"} {
		w,s := &memoryConsole{input:[]string{line}}, &memorySession{}
		if Serve(context.Background(),w,s) == nil || s.closes != 1 || len(s.operations) != 0 { t.Fatal("invalid input dispatched",line) }
	}
}
func TestConsoleUncertainCallNeverResendsOrCancels(t *testing.T) {
	s := &memorySession{failure: errors.New("fixture-private-error")}
	w := &memoryConsole{input:[]string{"start "+strings.Repeat("b",32)+"\n", "cancel "+strings.Repeat("b",32)+"\n"}}
	if Serve(context.Background(),w,s) == nil || len(s.operations) != 1 || s.closes != 1 { t.Fatal("uncertain call retried") }
	for _, b := range w.output { if strings.Contains(string(b),"fixture-private-error") { t.Fatal("raw error leaked") } }
}
