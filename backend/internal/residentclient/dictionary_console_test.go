package residentclient

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

type dictionaryMemory struct {
	memorySession
	reads []string
}

func (s *dictionaryMemory) List(_ context.Context, cursor string, n int) (json.RawMessage, error) {
	s.reads = append(s.reads, "list:"+cursor)
	return json.RawMessage(`{"error":"unsupported"}`), nil
}
func (s *dictionaryMemory) GetExecution(_ context.Context, id string) (json.RawMessage, error) {
	s.reads = append(s.reads, "get_execution:"+id)
	return json.RawMessage(`{"error":"not_found"}`), nil
}
func (s *dictionaryMemory) ReadOutput(_ context.Context, id string, c uint64, n int) (json.RawMessage, error) {
	s.reads = append(s.reads, "read_output:"+id)
	return json.RawMessage(`{"error":"result_released"}`), nil
}
func TestDictionaryConsoleSameSessionExplicitPages(t *testing.T) {
	s := &dictionaryMemory{}
	w := &memoryConsole{input: []string{"list - 32\n", "get_execution " + testID + "\n", "read_output " + testID + " 9007199254740991 4096\n", "result " + testID + "\n", "close\n", "list - 1\n"}}
	if e := Serve(context.Background(), w, s); e != nil || len(s.reads) != 3 || len(s.operations) != 1 || s.closes != 1 || w.reads != 5 {
		t.Fatal(e, s.reads, s.closes)
	}
	if !strings.Contains(string(w.output[1]), `"unsupported"`) || strings.Contains(string(w.output[1]), `"request_id"`) || !strings.Contains(string(w.output[3]), `"result_released"`) {
		t.Fatal("outcome erased")
	}
}
func TestDictionaryConsoleRejectsArgumentsBeforeDispatch(t *testing.T) {
	for _, line := range []string{"list - 0", "list - 33", "list - 01", "list 0 1", "list - +1", "list - 1 extra", "list  1", "get_execution", "get_execution " + testID + " extra", "read_output " + testID + " 0 0", "read_output " + testID + " 0 4097", "read_output " + testID + " 9007199254740992 1", "read_output " + testID + " -1 1", "read_output " + testID + " 01 1", "read_output " + testID + " 0 1.0"} {
		s := &dictionaryMemory{}
		w := &memoryConsole{input: []string{line + "\n"}}
		if Serve(context.Background(), w, s) == nil || len(s.reads) != 0 || len(s.operations) != 0 || s.closes != 1 {
			t.Fatal("invalid dispatch", line)
		}
	}
	s := &memorySession{}
	w := &memoryConsole{input: []string{"list - 1\n"}}
	if Serve(context.Background(), w, s) == nil || len(s.operations) != 0 || s.closes != 1 {
		t.Fatal("legacy port bypass")
	}
}
func TestDictionaryArgumentValidationNoHTTP(t *testing.T) {
	c := &client{}
	for _, call := range []func() (json.RawMessage, error){func() (json.RawMessage, error) { return c.List(context.Background(), "", 0) }, func() (json.RawMessage, error) { return c.List(context.Background(), "bad", 1) }, func() (json.RawMessage, error) { return c.GetExecution(context.Background(), "bad") }, func() (json.RawMessage, error) { return c.ReadOutput(context.Background(), testID, maxInteger+1, 1) }, func() (json.RawMessage, error) { return c.ReadOutput(context.Background(), testID, 0, 4097) }} {
		if _, e := call(); e != ErrProtocol {
			t.Fatal("invalid arguments")
		}
	}
}
func TestDictionaryDiscoveryRejectsMalformedCatalog(t *testing.T) {
	for _, raw := range []string{`{"tools":null}`, `{"tools":[],"nextCursor":"x"}`, `{"tools":[{"name":"rescue_fixed_list","inputSchema":{}} ,{"name":"rescue_fixed_list","inputSchema":{}}]}`, `{"tools":[{"name":"x"}]}`, `{"tools":[{"name":"x","inputSchema":{},"private":"x"}]}`} {
		if _, e := dictionaryNames([]byte(raw)); e == nil {
			t.Fatal("invalid discovery")
		}
	}
}
