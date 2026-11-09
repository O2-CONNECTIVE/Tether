package residentclient

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentgateway"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/mcpadapter"
)

const testSID = "abcdef0123456789abcdef0123456789"
const testID = "1234567890abcdef1234567890abcdef"

type clientFixture struct {
	mu sync.Mutex
	methods []string
	bad string
	violated bool
}

func (f *clientFixture) serve(w http.ResponseWriter,r *http.Request) {
	f.mu.Lock();defer f.mu.Unlock()
	if r.Header.Get("Authorization")!="Bearer anonymous-fixture" {f.violated=true}
	if r.Method==http.MethodDelete {
		f.methods=append(f.methods,"DELETE")
		if r.Header.Get(agentgateway.HeaderSession)!=testSID {f.violated=true}
		w.WriteHeader(http.StatusNoContent);return
	}
	var msg struct {ID uint64 `json:"id"`; Method string `json:"method"`; Params json.RawMessage `json:"params"`}
	if json.NewDecoder(r.Body).Decode(&msg)!=nil {f.violated=true;w.WriteHeader(400);return}
	f.methods=append(f.methods,msg.Method)
	if msg.Method!="initialize" && (r.Header.Get(agentgateway.HeaderSession)!=testSID || r.Header.Get(agentgateway.HeaderProtocol)!=mcpadapter.ProtocolVersion) {f.violated=true}
	if msg.Method=="notifications/initialized" {w.WriteHeader(http.StatusAccepted);return}
	w.Header().Set("Content-Type","application/json")
	var value any
	if msg.Method=="initialize" {
		if msg.ID!=1 || r.Header.Get(agentgateway.HeaderSession)!="" {f.violated=true}
		w.Header().Set(agentgateway.HeaderSession,testSID)
		value=map[string]any{"protocolVersion":mcpadapter.ProtocolVersion,"capabilities":map[string]any{"tools":map[string]any{"listChanged":false}},"serverInfo":map[string]any{"name":"rescue-mcp","version":"0.0.0-unreleased"},"instructions":"fixture"}
		if f.bad=="initialize-body" {_,_=w.Write([]byte(`{"jsonrpc":"2.0","id":9,"result":{}}`));return}
	} else {
		if msg.Method!="tools/call" || msg.ID!=uint64(len(f.methods)-1) {f.violated=true}
		var p struct {Name string `json:"name"`; Arguments map[string]string `json:"arguments"`}
		if json.Unmarshal(msg.Params,&p)!=nil || p.Arguments["request_id"]!=testID || len(p.Arguments)!=1 {f.violated=true}
		allowed:=false
		for _,op:=range []string{"start","result","cancel","release"} {allowed=allowed || p.Name==operation(op)}
		if !allowed {f.violated=true}
		switch f.bad {
		case "redirect": w.Header().Set("Location","https://outside.invalid/secret");w.WriteHeader(307);return
		case "auth": w.WriteHeader(401);_,_=w.Write([]byte("private-error"));return
		case "oversize": _,_=w.Write([]byte(strings.Repeat("x",mcpadapter.MaxLine+1)));return
		case "rpc-id": msg.ID++
		case "sid": w.Header().Set(agentgateway.HeaderSession,strings.Repeat("1",32))
		case "duplicate": _,_=w.Write([]byte(`{"jsonrpc":"2.0","id":2,"id":2,"result":{}}`));return
		}
		value=json.RawMessage(fixtureTool(map[string]any{"error":"not_found"}))
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc":"2.0","id":msg.ID,"result":value})
}

func TestInvalidInitializeRetainsOriginalValidSIDForOneClose(t *testing.T) {
	f:=&clientFixture{bad:"initialize-body"}
	s:=httptest.NewTLSServer(http.HandlerFunc(f.serve));defer s.Close()
	roots:=x509.NewCertPool();roots.AddCert(s.Certificate())
	c,e:=newClient(s.URL,[]byte("anonymous-fixture"),roots)
	if e!=nil || c.open(context.Background())==nil || c.sid!=testSID {t.Fatal("invalid initialization lost SID")}
	if c.open(context.Background())==nil || c.Close(context.Background())!=nil {t.Fatal("reset or wrong close")}
	f.mu.Lock();defer f.mu.Unlock()
	if f.violated || strings.Join(f.methods,",")!="initialize,DELETE" {t.Fatal("original close missing")}
}

func TestInitializeBodyFailureRetainsSIDAndClosesWithIndependentContext(t *testing.T) {
	for _,mode:=range []string{"truncated","deadline"} {
		t.Run(mode,func(t *testing.T){
			var mu sync.Mutex
			posts,deletes:=0,0
			wrong:=false
			bodyDone:=make(chan struct{})
			s:=httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter,r *http.Request){
				mu.Lock()
				if r.Header.Get("Authorization")!="Bearer anonymous-fixture" {wrong=true}
				if r.Method==http.MethodDelete {
					deletes++
					if r.Header.Get(agentgateway.HeaderSession)!=testSID {wrong=true}
					mu.Unlock();w.WriteHeader(http.StatusNoContent);return
				}
				posts++
				if r.Method!=http.MethodPost {wrong=true}
				mu.Unlock()
				defer close(bodyDone)
				w.Header().Set("Content-Type","application/json")
				w.Header().Set(agentgateway.HeaderSession,testSID)
				w.Header().Set("Content-Length","256")
				w.WriteHeader(http.StatusOK)
				_,_=w.Write([]byte(`{"jsonrpc":`))
				w.(http.Flusher).Flush()
				if mode=="deadline" {<-r.Context().Done()}
			}));defer s.Close()
			roots:=x509.NewCertPool();roots.AddCert(s.Certificate())
			c,e:=newClient(s.URL,[]byte("anonymous-fixture"),roots)
			if e!=nil {t.Fatal("construction")}
			ctx,cancel:=context.WithTimeout(context.Background(),time.Second)
			defer cancel()
			if c.open(ctx)==nil || c.sid!=testSID {t.Fatal("body uncertainty lost valid SID")}
			if mode=="deadline" && ctx.Err()!=context.DeadlineExceeded {t.Fatal("body deadline not exercised")}
			if _,e=c.Invoke(context.Background(),"start",testID);e==nil {t.Fatal("continued after body failure")}
			if c.open(context.Background())==nil {t.Fatal("reinitialized after body failure")}
			closing,stop:=context.WithTimeout(context.Background(),time.Second)
			defer stop()
			if c.Close(closing)!=nil || c.Close(closing)==nil {t.Fatal("original close not once")}
			select {case <-bodyDone:case <-time.After(time.Second):t.Fatal("original HTTP handler not joined")}
			mu.Lock();defer mu.Unlock()
			if wrong || posts!=1 || deletes!=1 {t.Fatal("extra request or wrong original SID")}
		})
	}
}

func fixtureClient(t *testing.T,f *clientFixture) *client {
	t.Helper()
	s:=httptest.NewTLSServer(http.HandlerFunc(f.serve));t.Cleanup(s.Close)
	roots:=x509.NewCertPool();roots.AddCert(s.Certificate())
	c,e:=newClient(s.URL,[]byte("anonymous-fixture"),roots)
	if e!=nil {t.Fatal("construction")}
	if e=c.open(context.Background());e!=nil {t.Fatal("initialize",e)}
	return c
}

func TestHTTPClientKeepsOneSIDAndExactFixedOperations(t *testing.T) {
	f:=&clientFixture{};c:=fixtureClient(t,f)
	for _,op:=range []string{"start","result","cancel","release"} {
		v,e:=c.Invoke(context.Background(),op,testID)
		if e!=nil || string(v)!=`{"error":"not_found"}` {t.Fatal("fixed call",op,e)}
	}
	if c.open(context.Background())==nil {t.Fatal("reinitialized")}
	if c.Close(context.Background())!=nil || c.Close(context.Background())==nil {t.Fatal("close not once")}
	f.mu.Lock();defer f.mu.Unlock()
	if f.violated || len(f.methods)!=7 || f.methods[0]!="initialize" || f.methods[6]!="DELETE" {t.Fatal("session/correlation changed",f.methods)}
}

func TestHTTPUncertaintyNeverRetriesRefreshesOrFollowsRedirect(t *testing.T) {
	for _,bad:=range []string{"redirect","auth","oversize","rpc-id","sid","duplicate"} {
		t.Run(bad,func(t *testing.T){
			f:=&clientFixture{bad:bad};c:=fixtureClient(t,f)
			if _,e:=c.Invoke(context.Background(),"start",testID);e==nil || strings.Contains(e.Error(),"private") {t.Fatal("uncertainty escaped")}
			if _,e:=c.Invoke(context.Background(),"cancel",testID);e==nil {t.Fatal("continued after uncertainty")}
			if c.open(context.Background())==nil {t.Fatal("authority reset")}
			if c.Close(context.Background())!=nil {t.Fatal("original session not closed")}
			f.mu.Lock();defer f.mu.Unlock()
			if f.violated || strings.Join(f.methods,",")!="initialize,notifications/initialized,tools/call,DELETE" {t.Fatal("unintended request",f.methods)}
		})
	}
}

func TestClientRefusesUntrustedInputBeforeNetwork(t *testing.T) {
	roots:=x509.NewCertPool()
	for _,u:=range []string{"http://example.invalid","https://u:p@example.invalid","https://example.invalid?x=1","https://example.invalid#x","https://example.invalid?","not-a-url"} {
		if c,e:=newClient(u,[]byte("anonymous-fixture"),roots);e==nil || c!=nil {t.Fatal("invalid endpoint accepted")}
	}
	for _,token:=range [][]byte{nil,[]byte("has space"),[]byte("line\nbreak"),[]byte(strings.Repeat("x",8193))} {
		if c,e:=newClient("https://example.invalid",token,roots);e==nil || c!=nil {t.Fatal("invalid token accepted")}
	}
	if c,e:=newClient("https://example.invalid",[]byte("fixture"),nil);e==nil || c!=nil {t.Fatal("implicit roots accepted")}
}
