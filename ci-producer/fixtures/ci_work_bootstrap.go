//go:build darwin && cgo && ci_actual_producer

package residentclient

import (
	"bufio"
	"context"
	"crypto/x509"
	"encoding/json"
	"io"
	"os"
	"sync/atomic"
	"time"

	"golang.org/x/sys/unix"
)

var ciWorkEntered atomic.Bool

// CIWorkMain exists only in the compiler overlay. FD3 carries synthetic test
// values, never a production credential input contract or authenticity proof.
func CIWorkMain() int {
	if !ciWorkEntered.CompareAndSwap(false,true) {return 1}
	f:=ciWorkPipe(3)
	if f==nil {return 1}
	if f.SetReadDeadline(time.Now().Add(5*time.Second))!=nil {_=f.Close();return 1}
	raw,e:=io.ReadAll(io.LimitReader(f,16385));closed:=f.Close()
	if e!=nil || closed!=nil || len(raw)>16384 {clear(raw);return 1}
	defer clear(raw)
	m,e:=fields(raw,"resource","token","ca")
	if e!=nil || !exact(m,"resource","token","ca") {return 1}
	var ca []byte
	if json.Unmarshal(m["ca"],&ca)!=nil {return 1}
	// x509 retains DER slices. The certificate must own bytes independently
	// before the decoded bootstrap buffer is cleared.
	cert,e:=x509.ParseCertificate(append([]byte(nil),ca...));clear(ca)
	if e!=nil || !cert.IsCA {return 1}
	roots:=x509.NewCertPool();roots.AddCert(cert)
	token:=[]byte(text(m["token"]))
	c,e:=newClient(text(m["resource"]),token,roots);clear(token)
	if e!=nil{return 1}
	input,output:=ciWorkPipe(0),ciWorkPipe(1)
	if input==nil || output==nil {return 1}
	defer input.Close();defer output.Close()
	ctx,cancel:=context.WithTimeout(context.Background(),5*time.Minute);defer cancel()
	if c.open(ctx)!=nil {
		closing,stop:=context.WithTimeout(context.Background(),5*time.Second)
		_ = c.Close(closing);stop()
		return 1
	}
	stream:=&ciWorkIO{input:input,output:output,reader:bufio.NewReaderSize(input,MaxInput+1)}
	if Serve(ctx,stream,c)!=nil{return 1}
	return 0
}

func ciWorkPipe(fd int) *os.File {
	var stat unix.Stat_t
	if unix.Fstat(fd,&stat)!=nil || stat.Mode&unix.S_IFMT!=unix.S_IFIFO || unix.SetNonblock(fd,true)!=nil {return nil}
	if _,e:=unix.FcntlInt(uintptr(fd),unix.F_SETFD,unix.FD_CLOEXEC);e!=nil{return nil}
	return os.NewFile(uintptr(fd),"owned-ci-work-pipe")
}

type ciWorkIO struct {input,output *os.File;reader *bufio.Reader}

// Join cancellation callbacks before another read/write can reuse the handle.
func ciWorkDeadline(ctx context.Context,set func(time.Time)error) (func(),error) {
	deadline,ok:=ctx.Deadline();if !ok || ctx.Err()!=nil{return nil,ErrUnavailable}
	if set(deadline)!=nil{return nil,ErrUnavailable}
	done:=make(chan struct{})
	stop:=context.AfterFunc(ctx,func(){_ = set(time.Now());close(done)})
	return func(){if !stop(){<-done}},nil
}

func (p *ciWorkIO) ReadLine(ctx context.Context) ([]byte,error) {
	end,e:=ciWorkDeadline(ctx,p.input.SetReadDeadline);if e!=nil{return nil,e};defer end()
	b,e:=p.reader.ReadSlice('\n')
	if len(b)>MaxInput{return nil,ErrProtocol}
	return b,e
}

func (p *ciWorkIO) WriteLine(ctx context.Context,b []byte) error {
	end,e:=ciWorkDeadline(ctx,p.output.SetWriteDeadline);if e!=nil{return e};defer end()
	n,e:=p.output.Write(b)
	if e!=nil || n!=len(b){return ErrUnavailable}
	return nil
}
