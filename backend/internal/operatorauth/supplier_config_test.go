package operatorauth

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/bootstrapauth"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/protectedkey"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/serviceauthowner"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/serviceready"
	"github.com/O2-CONNECTIVE/rescue-host/backend/supervisor/ephemeral"
)

func supplierConfigFixture() (bootstrapauth.Profile, SIDPolicy, HelperSupplierConfig) {
	const supervisor = "S-1-5-80-1-2-3-4-5"
	const helper = "S-1-5-80-6-7-8-9-10"
	p := SIDPolicy{OperatorSID: "S-1-5-21-1-2-3-1001", GuestSID: "S-1-5-21-1-2-3-1002"}
	v := protectedkey.VolumeProfile{Root: `C:\`, Serial: 7, Filesystem: "NTFS"}
	nodes := func(path string) []protectedkey.NodePolicy {
		return []protectedkey.NodePolicy{
			{Path: `C:\`, OwnerSID: "S-1-5-18", ACL: []protectedkey.ACE{{SID: supervisor, Mask: 0x1f01ff}}},
			{Path: path, OwnerSID: "S-1-5-18", ACL: []protectedkey.ACE{{SID: supervisor, Mask: 0x1f01ff}}},
		}
	}
	image := func(path string) bootstrapauth.Image {
		return bootstrapauth.Image{Path: path, SHA256: [32]byte{1}, MaxBytes: 1024, Volume: v, Nodes: nodes(path)}
	}
	parent := bootstrapauth.Profile{ServiceName: "SyntheticSupervisor", Keys: protectedkey.Profile{Version: 1, ProcessUserSID: "S-1-5-18", ServiceSID: supervisor, OperatorSID: p.OperatorSID, GuestSID: p.GuestSID, Volume: v, Provisioning: protectedkey.ProvisioningRecord{Reference: "synthetic", ProtectedAt: time.Unix(1, 0)}, Catalog: map[string]protectedkey.KeyProfile{"key": {Path: `C:\key`, Fingerprint: "SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", Nodes: nodes(`C:\key`)}}}, SupervisorImage: image(`C:\supervisor.exe`), ChildImage: image(`C:\backend.exe`)}
	security := func(mask uint32) serviceready.SecurityPolicy {
		return serviceready.SecurityPolicy{OwnerSID: "S-1-5-18", Protected: true, ACL: []protectedkey.ACE{{SID: "S-1-5-18", Mask: mask}}}
	}
	c := HelperSupplierConfig{ServiceName: "SyntheticHelper", ServiceSID: helper, ServiceCommandLine: `"C:\helper.exe" --auth-supplier-internal`, Image: image(`C:\helper.exe`), ServiceSecurity: security(0xf01ff), ProcessSecurity: security(0x1fffff), TokenSecurity: security(0xf01ff), ChildProcessSDDL: "D:P(A;;GA;;;S-1-5-18)(A;;GA;;;" + supervisor + ")(A;;0x120400;;;" + helper + ")", ChildThreadSDDL: "D:P(A;;GA;;;S-1-5-18)(A;;GA;;;" + supervisor + ")(A;;0x120040;;;" + helper + ")", Cleanup: time.Second, RunBudget: time.Second}
	return parent, p, c
}

func TestSupplierConfigRetainsFixedRecordWithoutEnablingNativeEntry(t *testing.T) {
	parent, p, c := supplierConfigFixture()
	if err := ValidateHelperSupplierConfig(parent, p, c); err != nil {
		t.Fatal(err)
	}
	f, err := NewSIDEndpointFactoryWithSupplier(new(bootstrapauth.Factory), 17, p.OperatorSID, p, parent, c)
	if err != nil || f == nil || f.RuntimeConfigured() {
		t.Fatal("configuration is not native source proof", err)
	}
	issuer, err := ephemeral.New()
	if err != nil {
		t.Fatal(err)
	}
	_, owner, err := serviceauthowner.New(issuer, new(sync.Mutex))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = owner.Revoke() })
	if b, err := f.Prepare(new(serviceready.SoleInput), owner, issuer); b != nil || !errors.Is(err, ErrUnconfigured) {
		t.Fatal("configured supplier opened public preparation", err)
	}
	c.Image.Nodes[0].ACL[0].SID = p.GuestSID
	c.ProcessSecurity.ACL[0].SID = p.GuestSID
	if f.supplier.Image.Nodes[0].ACL[0].SID == p.GuestSID || f.supplier.ProcessSecurity.ACL[0].SID == p.GuestSID {
		t.Fatal("caller changed retained policy")
	}
}

// supplierChildAccepts runs the helper witness's matches over an observation
// whose supplier token owner/DACL is the Record value, converted here rather
// than through the production conversion.
func supplierChildAccepts(parent bootstrapauth.Profile, c HelperSupplierConfig) bool {
	f := launchFixed()
	f.helperServiceSID, f.parentServiceSID = c.ServiceSID, parent.Keys.ServiceSID
	o := launchObservation()
	o.supplierToken.serviceGroup = helperServiceGroup{c.ServiceSID, 4}
	o.selfToken.serviceGroup = o.supplierToken.serviceGroup
	o.parentToken.serviceGroup = helperServiceGroup{parent.Keys.ServiceSID, 4}
	o.parentToken.acl = []helperLaunchACE{{mask: 0xf01ff, sid: "S-1-5-18"}, {mask: 0xf01ff, sid: parent.Keys.ServiceSID}, {mask: 0x20008, sid: c.ServiceSID}}
	o.supplierToken.owner, o.supplierToken.acl = c.TokenSecurity.OwnerSID, nil
	if c.TokenSecurity.ACL != nil {
		o.supplierToken.acl = []helperLaunchACE{}
	}
	for _, a := range c.TokenSecurity.ACL {
		o.supplierToken.acl = append(o.supplierToken.acl, helperLaunchACE{kind: a.Type, flags: a.Flags, mask: a.Mask, sid: a.SID})
	}
	l := &helperLaunchLease{fixed: f, loc: helperLaunchLocators{operatorRead: 9, reportWrite: 10, profileRead: 77, launchID: strings.Repeat("a", 64)}}
	return l.matches(o)
}

// One Record token policy is judged alike by the supervisor's Record check,
// its native snapshot check and the helper witness. Query-only grants stay
// valid; ownership or any dangerous right for shared LocalService, the operator
// or the supervisor's service SID is refused before startup on both sides.
func TestSupplierTokenPolicyJudgedAlikeByRecordSnapshotAndChild(t *testing.T) {
	const system = "S-1-5-18"
	parent, p, base := supplierConfigFixture()
	supervisor, helper := parent.Keys.ServiceSID, base.ServiceSID
	ace := func(sid string, mask uint32) protectedkey.ACE { return protectedkey.ACE{SID: sid, Mask: mask} }
	full := ace(system, 0xf01ff)
	many := make([]protectedkey.ACE, 33)
	for i := range many {
		many[i] = full
	}
	for name, tc := range map[string]struct {
		owner string
		acl   []protectedkey.ACE
		want  bool
	}{
		"system only":                 {system, []protectedkey.ACE{full}, true},
		"dedicated SID owns":          {helper, []protectedkey.ACE{full, ace(helper, 0xf01ff)}, true},
		"administrators own":          {"S-1-5-32-544", []protectedkey.ACE{full}, true},
		"shared user query only":      {system, []protectedkey.ACE{full, ace(supplierLocalService, 0x20008)}, true},
		"operator query only":         {system, []protectedkey.ACE{full, ace(p.OperatorSID, 0x8)}, true},
		"guest query only":            {system, []protectedkey.ACE{full, ace(p.GuestSID, 0x20008)}, true},
		"shared user denied":          {system, []protectedkey.ACE{{Type: 1, SID: supplierLocalService, Mask: 0xf01ff}, full}, true},
		"shared user owns":            {supplierLocalService, []protectedkey.ACE{full}, false},
		"operator owns":               {p.OperatorSID, []protectedkey.ACE{full}, false},
		"supervisor SID owns":         {supervisor, []protectedkey.ACE{full}, false},
		"guest owns":                  {p.GuestSID, []protectedkey.ACE{full}, false},
		"shared user assign primary":  {system, []protectedkey.ACE{full, ace(supplierLocalService, 0x1)}, false},
		"shared user duplicate":       {system, []protectedkey.ACE{full, ace(supplierLocalService, 0x2)}, false},
		"shared user impersonate":     {system, []protectedkey.ACE{full, ace(supplierLocalService, 0x4)}, false},
		"shared user adjust default":  {system, []protectedkey.ACE{full, ace(supplierLocalService, 0x80)}, false},
		"operator full":               {system, []protectedkey.ACE{full, ace(p.OperatorSID, 0xf01ff)}, false},
		"supervisor SID write DACL":   {system, []protectedkey.ACE{full, ace(supervisor, 0x40000)}, false},
		"guest duplicate":             {system, []protectedkey.ACE{full, ace(p.GuestSID, 0x20008|0x2)}, false},
		"null DACL":                   {system, nil, false},
		"inherited ACE":               {system, []protectedkey.ACE{{SID: system, Mask: 0xf01ff, Flags: 0x10}}, false},
		"unknown ACE type":            {system, []protectedkey.ACE{full, {Type: 2, SID: system, Mask: 0x8}}, false},
		"generic right outside known": {system, []protectedkey.ACE{ace(system, 0x10000000)}, false},
		"too many ACEs":               {system, many, false},
	} {
		t.Run(name, func(t *testing.T) {
			c := CloneHelperSupplierConfig(base)
			c.TokenSecurity = serviceready.SecurityPolicy{OwnerSID: tc.owner, ACL: tc.acl}
			record := ValidateHelperSupplierConfig(parent, p, c) == nil
			child := supplierChildAccepts(parent, c)
			// The parent's native snapshot equals this Record, so only the shared
			// predicate can decide; Record validation is bypassed here.
			snapshot := supplierSnapshotValid(c, supplierGeneration{pid: 123, creation: 456}, supplierSnapshotFixture(c))
			if record != tc.want || child != tc.want || snapshot != tc.want {
				t.Fatalf("record=%v child=%v snapshot=%v want=%v", record, child, snapshot, tc.want)
			}
			if tc.want {
				return
			}
			if f, err := NewSIDEndpointFactoryWithSupplier(new(bootstrapauth.Factory), 17, p.OperatorSID, p, parent, c); f != nil || !errors.Is(err, ErrUnconfigured) {
				t.Fatal("invalid token policy configured the factory", err)
			}
			k := &supplierFixture{}
			if s, err := newSupplierLease(parent, p, c, k); s != nil || !errors.Is(err, ErrUnconfigured) || k.acquires != 0 {
				t.Fatal("invalid token policy reached supplier startup", err)
			}
		})
	}
}

// supplierObjectCase sets one supplier object's owner/DACL; the SYSTEM ACE that
// the supervisor needs is always present.
type supplierObjectCase struct {
	name    string
	process bool
	owner   string
	acl     []protectedkey.ACE
	want    bool
}

func (tc supplierObjectCase) apply(c HelperSupplierConfig) HelperSupplierConfig {
	c = CloneHelperSupplierConfig(c)
	system := protectedkey.ACE{SID: "S-1-5-18", Mask: 0xf01ff}
	if tc.process {
		system.Mask = 0x1fffff
	}
	s := serviceready.SecurityPolicy{OwnerSID: tc.owner, Protected: true, ACL: append([]protectedkey.ACE{system}, tc.acl...)}
	if tc.process {
		c.ProcessSecurity = s
	} else {
		c.ServiceSecurity = s
	}
	return c
}

// checkSupplierObjectCase judges the Record and, independently, a native
// snapshot that equals it, so only the shared predicate decides the snapshot.
// A refused policy reaches neither the factory nor supplier startup.
func checkSupplierObjectCase(t *testing.T, parent bootstrapauth.Profile, p SIDPolicy, c HelperSupplierConfig, want bool) {
	t.Helper()
	record := ValidateHelperSupplierConfig(parent, p, c) == nil
	snapshot := supplierSnapshotValid(c, supplierGeneration{pid: 123, creation: 456}, supplierSnapshotFixture(c))
	if record != want || snapshot != want {
		t.Fatalf("record=%v snapshot=%v want=%v", record, snapshot, want)
	}
	if want {
		return
	}
	if f, err := NewSIDEndpointFactoryWithSupplier(new(bootstrapauth.Factory), 17, p.OperatorSID, p, parent, c); f != nil || !errors.Is(err, ErrUnconfigured) {
		t.Fatal("dangerous object policy configured the factory", err)
	}
	k := &supplierFixture{}
	if s, err := newSupplierLease(parent, p, c, k); s != nil || !errors.Is(err, ErrUnconfigured) || k.acquires != 0 {
		t.Fatal("dangerous object policy reached supplier startup", err)
	}
}

// Only SYSTEM and Administrators control the service object; on the process
// object the dedicated SID may also own and use itself. Shared LocalService,
// the operator, the supervisor's service SID and the guest may only query.
func TestSupplierServiceAndProcessPolicyJudgedByRecordAndSnapshot(t *testing.T) {
	const system, admins, shared = "S-1-5-18", "S-1-5-32-544", supplierLocalService
	parent, p, base := supplierConfigFixture()
	supervisor, helper, operator, guest := parent.Keys.ServiceSID, base.ServiceSID, p.OperatorSID, p.GuestSID
	ace := func(sid string, mask uint32) []protectedkey.ACE { return []protectedkey.ACE{{SID: sid, Mask: mask}} }
	for _, tc := range []supplierObjectCase{
		{"service system start stop", false, system, ace(system, 0x20035), true},
		{"service administrators own", false, admins, nil, true},
		{"service helper status", false, system, ace(helper, 0x4), true},
		{"service operator query", false, system, ace(operator, 0x2008d), true},
		{"service shared user query", false, system, ace(shared, 0x2008d), true},
		{"service supervisor SID query", false, system, ace(supervisor, 0x2008d), true},
		{"service guest query", false, system, ace(guest, 0x4), true},
		{"service shared user denied", false, system, []protectedkey.ACE{{Type: 1, SID: shared, Mask: 0xf01ff}}, true},
		{"service shared change config", false, system, ace(shared, 0x2), false},
		{"service shared generic all", false, system, ace(shared, 0x10000000), false},
		{"service operator start stop", false, system, ace(operator, 0x30), false},
		{"service supervisor write DAC", false, system, ace(supervisor, 0x40000), false},
		{"service helper change config", false, system, ace(helper, 0x2), false},
		{"service helper start", false, system, ace(helper, 0x10), false},
		{"service helper write DAC", false, system, ace(helper, 0x40000), false},
		{"service guest start stop", false, system, ace(guest, 0x30), false},
		{"service helper owns", false, helper, nil, false},
		{"service shared user owns", false, shared, nil, false},
		{"service operator owns", false, operator, nil, false},
		{"service unknown right", false, system, ace(system, 0x01000000), false},
		{"process system query", true, system, ace(system, 0x121000), true},
		{"process administrators own", true, admins, nil, true},
		{"process helper query", true, system, ace(helper, 0x101000), true},
		{"process helper full", true, system, ace(helper, 0x1fffff), true},
		{"process helper owns", true, helper, nil, true},
		{"process operator query", true, system, ace(operator, 0x121400), true},
		{"process shared user query", true, system, ace(shared, 0x101000), true},
		{"process shared create thread", true, system, ace(shared, 0x2), false},
		{"process shared VM write", true, system, ace(shared, 0x28), false},
		{"process shared duplicate", true, system, ace(shared, 0x40), false},
		{"process shared terminate", true, system, ace(shared, 0x1), false},
		{"process operator full", true, system, ace(operator, 0x1fffff), false},
		{"process supervisor write DAC", true, system, ace(supervisor, 0x40000), false},
		{"process guest terminate", true, system, ace(guest, 0x1), false},
		{"process shared user owns", true, shared, nil, false},
		{"process operator owns", true, operator, nil, false},
		{"process supervisor SID owns", true, supervisor, nil, false},
		{"process unknown right", true, system, ace(system, 0x01000000), false},
	} {
		t.Run(tc.name, func(t *testing.T) { checkSupplierObjectCase(t, parent, p, tc.apply(base), tc.want) })
	}
}

// The default DACL governs later objects of any kind created without explicit
// security, so others keep only standard read/sync. This pure boundary is the
// one the Windows reader applies to the held token's ACEs.
func TestSupplierDefaultACLKeepsQueryOnlyForOthers(t *testing.T) {
	parent, p, c := supplierConfigFixture()
	supervisor, helper := parent.Keys.ServiceSID, c.ServiceSID
	full := protectedkey.ACE{SID: "S-1-5-18", Mask: 0x10000000}
	for _, tc := range []struct {
		name string
		acl  []protectedkey.ACE
		want bool
	}{
		{"system generic all", []protectedkey.ACE{full}, true},
		{"helper generic all", []protectedkey.ACE{full, {SID: helper, Mask: 0x10000000}}, true},
		{"unknown read sync", []protectedkey.ACE{full, {SID: "S-1-5-21-9-9-9-9", Mask: 0x120000}}, true},
		{"shared user denied", []protectedkey.ACE{{Type: 1, SID: supplierLocalService, Mask: 0x10000000}, full}, true},
		{"shared user generic all", []protectedkey.ACE{full, {SID: supplierLocalService, Mask: 0x10000000}}, false},
		{"operator full", []protectedkey.ACE{full, {SID: p.OperatorSID, Mask: 0x1f0fff}}, false},
		{"supervisor SID generic all", []protectedkey.ACE{full, {SID: supervisor, Mask: 0x10000000}}, false},
		{"guest object specific", []protectedkey.ACE{full, {SID: p.GuestSID, Mask: 0x1}}, false},
		{"unknown right outside known", []protectedkey.ACE{{SID: "S-1-5-18", Mask: 0x01000000}}, false},
		{"inherited ACE", []protectedkey.ACE{{SID: "S-1-5-18", Mask: 0x10000000, Flags: 0x10}}, false},
		{"unknown ACE type", []protectedkey.ACE{full, {Type: 2, SID: "S-1-5-18", Mask: 0x1}}, false},
		{"null DACL", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := supplierDefaultACLProtected(tc.acl, c); got != tc.want {
				t.Fatalf("got=%v want=%v", got, tc.want)
			}
		})
	}
}

// OperatorSID validation is syntactic, so a configured value may name a group
// such as the machine's domain users (RID 513), which a guest may hold. This
// does not show an exploit; it shows that dangerous supplier rights are refused
// without relying on the OperatorSID being a user, while query-only stays valid.
func TestSupplierPolicyRefusesOperatorGroupDangerousRights(t *testing.T) {
	parent, p, base := supplierConfigFixture()
	group := "S-1-5-21-1-2-3-513"
	p.OperatorSID, parent.Keys.OperatorSID = group, group
	if err := ValidateHelperSupplierConfig(parent, p, base); err != nil {
		t.Fatal("group-shaped OperatorSID fixture was not accepted syntactically", err)
	}
	ace := func(mask uint32) []protectedkey.ACE { return []protectedkey.ACE{{SID: group, Mask: mask}} }
	for _, tc := range []supplierObjectCase{
		{"service query", false, "S-1-5-18", ace(0x2008d), true},
		{"service start stop", false, "S-1-5-18", ace(0x30), false},
		{"service change config", false, "S-1-5-18", ace(0x2), false},
		{"service owns", false, group, nil, false},
		{"process query", true, "S-1-5-18", ace(0x121400), true},
		{"process VM write", true, "S-1-5-18", ace(0x28), false},
		{"process terminate", true, "S-1-5-18", ace(0x1), false},
		{"process owns", true, group, nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) { checkSupplierObjectCase(t, parent, p, tc.apply(base), tc.want) })
	}
	full := protectedkey.ACE{SID: "S-1-5-18", Mask: 0x10000000}
	if !supplierDefaultACLProtected([]protectedkey.ACE{full, {SID: group, Mask: 0x120000}}, base) ||
		supplierDefaultACLProtected([]protectedkey.ACE{full, {SID: group, Mask: 0x10000000}}, base) {
		t.Fatal("default DACL judged the group-shaped OperatorSID by its role")
	}
	token := CloneHelperSupplierConfig(base)
	token.TokenSecurity.ACL = append(token.TokenSecurity.ACL, protectedkey.ACE{SID: group, Mask: 0x2})
	if ValidateHelperSupplierConfig(parent, p, token) == nil || supplierChildAccepts(parent, token) {
		t.Fatal("group-shaped OperatorSID could duplicate the supplier token")
	}
}

func TestSupplierConfigRejectsGuestStartStopRights(t *testing.T) {
	parent, p, c := supplierConfigFixture()
	// SERVICE_START|SERVICE_STOP would let a guest create or end a generation.
	c.ServiceSecurity.ACL = append(c.ServiceSecurity.ACL, protectedkey.ACE{SID: p.GuestSID, Mask: 0x30})
	if err := ValidateHelperSupplierConfig(parent, p, c); err == nil {
		t.Fatal("guest start/stop right accepted")
	}
}
