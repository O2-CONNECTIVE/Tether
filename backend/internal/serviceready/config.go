// Package serviceready はsessionを持たないservice所有listenerの準備を検証する。
// listener準備はoperator命令の認可や接続プロトコルの実装ではない。
package serviceready

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/bootstrapauth"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/protectedkey"
)

var (
	ErrConfig      = errors.New("service readyの保護設定が不足しています")
	ErrDenied      = errors.New("service readyのkernel証拠を確認できません")
	ErrUnconfirmed = errors.New("service readyの回収を確認できません")
	ErrUnavailable = errors.New("service readyのnative APIを利用できません")
	ErrFatal       = bootstrapauth.ErrFatal
)

type SecurityPolicy struct {
	OwnerSID  string
	ACL       []protectedkey.ACE
	Protected bool
}
type Config struct {
	Profile                          bootstrapauth.Profile
	PrimaryToken                     uintptr
	Endpoint, ServiceCommandLine     string
	ServiceSecurity, ProcessSecurity SecurityPolicy
	CleanupTimeout                   time.Duration
}
type Factory struct {
	c          Config
	auth       *bootstrapauth.Factory
	api        kernel
	configured bool
}

// NewNative は静的検証とdeep copy専用で、OS照会やlistener作成を行わない。
func NewNative(c Config) (*Factory, error) {
	auth, err := bootstrapauth.NewNativeFactory(c.Profile)
	path := c.Profile.SupervisorImage.Path
	exact := c.ServiceCommandLine == `"`+path+`"` || !strings.ContainsAny(path, " \t") && c.ServiceCommandLine == path
	if err != nil || !exact || path == "" || c.Endpoint != `\\.\pipe\rescue-service-`+c.Profile.ServiceName || len(c.Endpoint) > 256 || c.PrimaryToken == 0 || c.PrimaryToken >= ^uintptr(15) || c.CleanupTimeout <= 0 || !validSecurity(c.Profile, c.ServiceSecurity, false) || !validSecurity(c.Profile, c.ProcessSecurity, true) {
		return nil, ErrConfig
	}
	c = cloneConfig(c)
	return &Factory{c: c, auth: auth, configured: true}, nil
}
func cloneSecurity(s SecurityPolicy) SecurityPolicy {
	if s.ACL != nil {
		s.ACL = append(make([]protectedkey.ACE, 0, len(s.ACL)), s.ACL...)
	}
	return s
}
func cloneNodes(n []protectedkey.NodePolicy) []protectedkey.NodePolicy {
	out := append([]protectedkey.NodePolicy(nil), n...)
	for i := range out {
		out[i].ACL = append([]protectedkey.ACE(nil), out[i].ACL...)
	}
	return out
}
func cloneConfig(c Config) Config {
	c.ServiceSecurity = cloneSecurity(c.ServiceSecurity)
	c.ProcessSecurity = cloneSecurity(c.ProcessSecurity)
	c.Profile.SupervisorImage.Nodes = cloneNodes(c.Profile.SupervisorImage.Nodes)
	c.Profile.ChildImage.Nodes = cloneNodes(c.Profile.ChildImage.Nodes)
	catalog := make(map[string]protectedkey.KeyProfile, len(c.Profile.Keys.Catalog))
	for ref, key := range c.Profile.Keys.Catalog {
		key.Nodes = cloneNodes(key.Nodes)
		catalog[ref] = key
	}
	c.Profile.Keys.Catalog = catalog
	return c
}

// CheckServiceCallerは元SCM callerを移送前に拒否するためだけのguard。
// proof/owned refsを返さず、workerのPrepareやoperator server認証を代替しない。
func (f *Factory) CheckServiceCaller(ctx context.Context) error {
	if f == nil || !f.configured || f.auth == nil {
		return ErrConfig
	}
	if f.api != nil {
		return f.api.checkCaller(ctx)
	}
	return f.auth.CheckServiceCaller(ctx)
}
