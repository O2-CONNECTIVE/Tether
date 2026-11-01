package serviceready

import (
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/protectedkey"
	"testing"
)

func TestStaticPoliciesRejectUnknownMutationAndDoNotOffsetAllowWithDeny(t *testing.T) {
	const unknown = "S-1-5-21-9"
	for _, tc := range []struct {
		process bool
		mask    uint32
	}{{false, 2}, {false, 0x20}, {false, 0x10}, {false, 0x100}, {false, 0x40000}, {false, 0x10000000}, {true, 0x40}, {true, 0x10}, {true, 0x20}, {true, 8}, {true, 1}, {true, 0x800}} {
		c := config()
		policy := &c.ServiceSecurity
		if tc.process {
			policy = &c.ProcessSecurity
		}
		policy.ACL = append(policy.ACL, protectedkey.ACE{Type: 1, SID: unknown, Mask: tc.mask}, protectedkey.ACE{SID: unknown, Mask: tc.mask})
		if f, err := NewNative(c); f != nil || err != ErrConfig {
			t.Fatal("未知主体へ停止/改変/VM/handle権限を許可", tc, f, err)
		}
	}
	c := config()
	c.ServiceSecurity.ACL = append(c.ServiceSecurity.ACL, protectedkey.ACE{SID: unknown, Mask: 0x2008d})
	c.ProcessSecurity.ACL = append(c.ProcessSecurity.ACL, protectedkey.ACE{SID: unknown, Mask: 0x120000})
	if f, err := NewNative(c); f == nil || err != nil {
		t.Fatal("許可したservice query/process security+sync権限まで拒否", err)
	}
	c.ServiceSecurity.OwnerSID = operator
	c.ProcessSecurity.OwnerSID = operator
	c.ServiceSecurity.ACL = append(c.ServiceSecurity.ACL, protectedkey.ACE{SID: operator, Mask: 0xf01ff})
	c.ProcessSecurity.ACL = append(c.ProcessSecurity.ACL, protectedkey.ACE{SID: operator, Mask: 0x1fffff})
	if f, err := NewNative(c); f == nil || err != nil {
		t.Fatal("信頼されたhost operatorを拒否", err)
	}
}
func TestStaticConfigRejectsAmbiguousCommandLineEndpointAndSecurity(t *testing.T) {
	for _, mutate := range []func(*Config){
		func(c *Config) { c.ServiceCommandLine += " --anything" }, func(c *Config) { c.ServiceCommandLine = "" },
		func(c *Config) { c.Endpoint = `\\.\pipe\rescue-service-Another` }, func(c *Config) { c.PrimaryToken = 0 },
		func(c *Config) { c.ServiceSecurity.OwnerSID = guest }, func(c *Config) { c.ProcessSecurity.OwnerSID = "S-1-5-21-9" },
		func(c *Config) { c.ServiceSecurity.Protected = false }, func(c *Config) { c.ProcessSecurity.ACL = nil },
		func(c *Config) { c.ServiceSecurity.ACL[0].Flags = 8 }, func(c *Config) { c.ProcessSecurity.ACL[0].Type = 5 },
	} {
		c := config()
		mutate(&c)
		if f, err := NewNative(c); f != nil || err != ErrConfig {
			t.Fatal(f, err)
		}
	}
	c := config()
	c.ServiceCommandLine = c.Profile.SupervisorImage.Path
	if f, err := NewNative(c); f == nil || err != nil {
		t.Fatal("space無しbare exactを拒否", err)
	}
}
func TestChangedKernelEvidenceRevokesTheSamePlane(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*fakeKernel)
	}{
		{"service stopped", func(k *fakeKernel) { k.svc.value.state = 1 }},
		{"service stop pending", func(k *fakeKernel) { k.svc.value.state = 3 }},
		{"shared process", func(k *fakeKernel) { k.svc.value.kind = 0x20 }},
		{"binary config", func(k *fakeKernel) { k.svc.value.commandLine = `"C:\other.exe"` }},
		{"service ACL", func(k *fakeKernel) { k.svc.value.security.ACL[0].Mask = 0 }},
		{"process ACL", func(k *fakeKernel) { k.proc.value.security.Protected = false }},
		{"process creation", func(k *fakeKernel) { k.proc.value.creation++ }},
		{"process death", func(k *fakeKernel) { k.proc.value.alive = false }},
		{"pipe byte", func(k *fakeKernel) { k.listener.value.flags = 1 }},
		{"pipe mode", func(k *fakeKernel) { k.listener.value.mode = 0 }},
		{"pipe inherit", func(k *fakeKernel) { k.listener.value.handleFlags = 1 }},
		{"pipe server PID", func(k *fakeKernel) { k.listener.value.serverPID = 77 }},
		{"pipe security", func(k *fakeKernel) { k.listener.value.security.ACL[1].Mask = 0 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := config()
			k := fixture(c)
			f, _ := NewNative(c)
			f.api = k
			p, err := f.Prepare(testContext(t))
			if err != nil {
				t.Fatal(err)
			}
			tc.change(k)
			if err = p.Recheck(testContext(t)); err == nil {
				t.Fatal("変化したkernel証拠を受理")
			}
			if err = p.ConfirmRunning(testContext(t)); err == nil {
				t.Fatal("同じPlaneが復活")
			}
			k.listener.op.done = true
			if err = p.Close(testContext(t)); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestUnknownAndGuestProcessPolicyRejectsGenericAndMaximumAccess(t *testing.T) {
	for _, sid := range []string{"S-1-5-21-9", guest} {
		for _, mask := range []uint32{0x02000000, 0x80000000, 0x40000000, 0x20000000, 0x10000000} {
			c := config()
			c.ProcessSecurity.ACL = append(c.ProcessSecurity.ACL, protectedkey.ACE{Type: 1, SID: sid, Mask: mask}, protectedkey.ACE{SID: sid, Mask: mask})
			if f, err := NewNative(c); f != nil || err != ErrConfig {
				t.Errorf("未知/Guestへgeneric等を許可: SID=%s mask=%#x factoryConfigured=%t error=%v", sid, mask, f != nil, err)
			}
		}
		c := config()
		c.ProcessSecurity.ACL = append(c.ProcessSecurity.ACL, protectedkey.ACE{SID: sid, Mask: 0x120000})
		if f, err := NewNative(c); f == nil || err != nil {
			t.Fatal("READ_CONTROL/SYNCHRONIZEまで拒否", sid, err)
		}
	}
	for _, sid := range []string{user, service, operator, "S-1-5-18", "S-1-5-32-544"} {
		c := config()
		c.ProcessSecurity.ACL = append(c.ProcessSecurity.ACL, protectedkey.ACE{SID: sid, Mask: 0x121400})
		if f, err := NewNative(c); f == nil || err != nil {
			t.Fatal("trusted主体のqueryまで拒否", sid, err)
		}
	}
}
