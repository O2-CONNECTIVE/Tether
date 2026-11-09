//go:build darwin && cgo && ci_actual_producer

package residentpipe

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentwire"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/mcpadapter"
)

var errCIWork = errors.New("work child unconfirmed")

// The connection test retains this original owner on every uncertain path.
// No deferred kill, alternate waiter or new client adopts its HTTP session.
type ciWorkChild struct {
	cmd *exec.Cmd
	input,output,diagnostic *os.File
	launch []*os.File
	reader *bufio.Reader
	waitDone chan struct{}
	mu sync.Mutex
	waitErr error
}

func startCIWork(g *ciGateway,root string) (*ciWorkChild,error) {
	p:=&ciWorkChild{waitDone:make(chan struct{})}
	binary,expected:=os.Getenv("CI_WORK_BINARY"),os.Getenv("CI_WORK_SHA256")
	raw,e:=os.ReadFile(binary)
	if e!=nil{return p,errCIWork}
	sum:=sha256.Sum256(raw)
	if !filepath.IsAbs(binary) || hex.EncodeToString(sum[:])!=expected{return p,errCIWork}
	childIn,input,e:=os.Pipe();if e!=nil{return p,errCIWork}
	p.input=input;p.launch=append(p.launch,childIn)
	output,childOut,e:=os.Pipe();if e!=nil{return p,errCIWork}
	p.output=output;p.launch=append(p.launch,childOut)
	bootstrap,send,e:=os.Pipe();if e!=nil{return p,errCIWork}
	p.launch=append(p.launch,bootstrap,send)
	p.diagnostic,e=os.OpenFile(filepath.Join(root,"work.stderr"),os.O_WRONLY|os.O_CREATE|os.O_EXCL,0600)
	if e!=nil{return p,errCIWork}
	p.cmd=exec.Command(binary,"foreground")
	p.cmd.Env=[]string{"PATH=/usr/bin:/bin:/usr/sbin:/sbin","LANG=C"}
	p.cmd.Dir=root;p.cmd.Stdin=childIn;p.cmd.Stdout=childOut;p.cmd.Stderr=p.diagnostic
	p.cmd.ExtraFiles=[]*os.File{bootstrap}
	if p.cmd.Start()!=nil{return p,errCIWork}
	go func(){e:=p.cmd.Wait();p.mu.Lock();p.waitErr=e;p.mu.Unlock();close(p.waitDone)}()
	for _,f:=range []*os.File{childIn,childOut,bootstrap} {if f.Close()!=nil{return p,errCIWork}}
	p.launch=[]*os.File{send}
	raw,e=json.Marshal(map[string]any{"resource":g.resource,"token":g.token,"ca":g.caDER})
	if e!=nil || len(raw)>16384{return p,errCIWork}
	defer clear(raw)
	if send.SetWriteDeadline(time.Now().Add(5*time.Second))!=nil{return p,errCIWork}
	n,e:=send.Write(raw);if e!=nil || n!=len(raw) || send.Close()!=nil{return p,errCIWork}
	p.launch=nil;p.reader=bufio.NewReaderSize(output,mcpadapter.MaxLine+1)
	message,e:=p.read()
	if e!=nil || len(message)!=2 || message["type"]!="ready" || message["protocolVersion"]!=mcpadapter.ProtocolVersion{return p,errCIWork}
	return p,nil
}

func (p *ciWorkChild) send(line string) error {
	if p.input.SetWriteDeadline(time.Now().Add(5*time.Second))!=nil{return errCIWork}
	n,e:=p.input.Write([]byte(line))
	if e!=nil || n!=len(line){return errCIWork};return nil
}

func (p *ciWorkChild) read() (map[string]any,error) {
	if p.output.SetReadDeadline(time.Now().Add(40*time.Second))!=nil{return nil,errCIWork}
	line,e:=p.reader.ReadSlice('\n')
	if e!=nil || len(line)>mcpadapter.MaxLine+1 || agentwire.CheckJSON(line,false)!=nil{return nil,errCIWork}
	var value map[string]any
	if json.Unmarshal(line,&value)!=nil || value==nil{return nil,errCIWork}
	return value,nil
}

func (p *ciWorkChild) tool(g *ciGateway,name,id string) map[string]any {
	g.t.Helper();g.diagnostic.beginTool(name)
	op:=map[string]string{"rescue_fixed_start":"start","rescue_fixed_status":"result","rescue_fixed_cancel":"cancel","rescue_fixed_release":"release"}[name]
	if op=="" || !agentwire.ValidCommandID(id) || p.send(op+" "+id+"\n")!=nil{g.diagnostic.fail(g.t,"work-cli-write")}
	v,e:=p.read()
	value,ok:=v["value"].(map[string]any)
	if e!=nil || len(v)!=4 || v["type"]!="result" || v["operation"]!=op || v["request_id"]!=id || !ok {g.diagnostic.fail(g.t,"work-cli-response")}
	result:=map[string]any{"structuredContent":value,"isError":value["error"]!=""}
	g.diagnostic.observeTool(result)
	if code:=ciToolResultFailure(result);code!=""{g.diagnostic.fail(g.t,code)}
	return value
}

func (p *ciWorkChild) finish() error {
	// Later bytes in the same actual pipe write must not run after close.
	if p.send("close\nstart ffffffffffffffffffffffffffffffff\nlist - 1\nget_execution ffffffffffffffffffffffffffffffff\nread_output ffffffffffffffffffffffffffffffff 0 7\n")!=nil{return errCIWork}
	v,e:=p.read()
	if e!=nil || len(v)!=3 || v["type"]!="closed" || v["session"]!="confirmed" || v["work_cleanup"]!="not_observed" {return errCIWork}
	if p.input.Close()!=nil || p.output.SetReadDeadline(time.Now().Add(10*time.Second))!=nil{return errCIWork}
	line,e:=p.reader.ReadSlice('\n')
	if !errors.Is(e,io.EOF) || len(line)!=0{return errCIWork}
	select {case <-p.waitDone:case <-time.After(10*time.Second):return errCIWork}
	p.mu.Lock();e=p.waitErr;p.mu.Unlock()
	if e!=nil || p.output.Close()!=nil || p.diagnostic.Close()!=nil{return errCIWork}
	return nil
}
