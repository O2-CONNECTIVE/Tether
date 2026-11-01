package operatorauth

import (
	"crypto/rand"
	"encoding/hex"
	"strings"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/bootstrapauth"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/protectedkey"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/serviceready"
)

// HelperSupplierConfig is fixed provisioning data, not authentication proof.
// It names a registered, stopped, demand-start dedicated LocalService supplier.
// One request owner starts it, borrows its token and stops only the generation
// it started. Registration and its removal belong to the provisioning owner.
type HelperSupplierConfig struct {
	ServiceName, ServiceSID, ServiceCommandLine     string
	Image                                           bootstrapauth.Image
	ServiceSecurity, ProcessSecurity, TokenSecurity serviceready.SecurityPolicy
	ChildProcessSDDL, ChildThreadSDDL               string
	Cleanup, RunBudget                              time.Duration
}

func ValidateHelperSupplierConfig(parent bootstrapauth.Profile, policy SIDPolicy, c HelperSupplierConfig) error {
	if parent.Keys.ProcessUserSID != "S-1-5-18" || !policy.valid() || parent.Keys.OperatorSID != policy.OperatorSID || parent.Keys.GuestSID != policy.GuestSID || c.ServiceName == parent.ServiceName || c.ServiceSID == parent.Keys.ServiceSID || c.ServiceSID == policy.GuestSID || c.ServiceSID == policy.OperatorSID {
		return ErrUnconfigured
	}
	if len(c.ServiceName) == 0 || len(c.ServiceName) > 256 {
		return ErrUnconfigured
	}
	for _, r := range c.ServiceName {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-') {
			return ErrUnconfigured
		}
	}
	if !reportSIDValid(c.ServiceSID) || !strings.HasPrefix(c.ServiceSID, "S-1-5-80-") || len(strings.Split(c.ServiceSID, "-")) != 9 || c.ServiceCommandLine != `"`+c.Image.Path+`" --auth-supplier-internal` || c.Cleanup <= 0 || c.Cleanup > time.Minute || c.RunBudget <= 0 || c.RunBudget > time.Minute {
		return ErrUnconfigured
	}
	check := parent
	check.ChildImage = c.Image
	if _, err := bootstrapauth.NewNativeFactory(check); err != nil {
		return ErrUnconfigured
	}
	// Start/stop and configuration stay with the SYSTEM supervisor; everyone
	// else, the dedicated SID included, may only query the service object.
	if !supplierServiceProtected(c) || !c.ServiceSecurity.Protected || !supplierProcessProtected(c) || !c.ProcessSecurity.Protected || !supplierTokenProtected(c.TokenSecurity, c.ServiceSID) {
		return ErrUnconfigured
	}
	prefix := "D:P(A;;GA;;;S-1-5-18)(A;;GA;;;" + parent.Keys.ServiceSID + ")"
	if c.ChildProcessSDDL != prefix+"(A;;0x120400;;;"+c.ServiceSID+")" || c.ChildThreadSDDL != prefix+"(A;;0x120040;;;"+c.ServiceSID+")" {
		return ErrUnconfigured
	}
	return nil
}

// supplierACLProtected is the one rule for the supplier's service object,
// process object and token default DACL. Only SYSTEM, Administrators and a
// non-empty self may be granted more than query. Shared LocalService, the
// operator, the supervisor's service SID and the guest may only query, whether
// a SID names a user or a group. Unknown rights, ACE types and flags are refused.
func supplierACLProtected(acl []protectedkey.ACE, self string, known, query uint32) bool {
	if acl == nil || len(acl) > 128 {
		return false
	}
	for _, a := range acl {
		if a.Type > 1 || a.Flags != 0 || a.Mask == 0 || a.Mask&^known != 0 || !reportSIDValid(a.SID) ||
			a.Type == 0 && !supplierOwnerTrusted(a.SID, self) && a.Mask&^query != 0 {
			return false
		}
	}
	return true
}

func supplierOwnerTrusted(sid, self string) bool {
	return sid == "S-1-5-18" || sid == "S-1-5-32-544" || self != "" && sid == self
}

func supplierSecurityProtected(s serviceready.SecurityPolicy, self string, known, query uint32) bool {
	return reportSIDValid(s.OwnerSID) && supplierOwnerTrusted(s.OwnerSID, self) && supplierACLProtected(s.ACL, self, known, query)
}

// The SYSTEM supervisor needs start/stop/config; the helper only queries status.
// The dedicated SID gets no control of its own service, so a compromised helper
// cannot reconfigure it.
func supplierServiceProtected(c HelperSupplierConfig) bool {
	return supplierSecurityProtected(c.ServiceSecurity, "", 0xf00f01ff, 0x2008d)
}

// The supplier may own its own process; no other principal gets terminate,
// thread, VM, handle-duplication or DACL rights.
func supplierProcessProtected(c HelperSupplierConfig) bool {
	return supplierSecurityProtected(c.ProcessSecurity, c.ServiceSID, 0xf01fffff, 0x121400)
}

// supplierDefaultACLProtected judges the held token's default DACL ACEs. It
// applies to any later object kind, so others keep only standard read/sync.
// TokenOwner is not judged here.
func supplierDefaultACLProtected(acl []protectedkey.ACE, c HelperSupplierConfig) bool {
	return supplierACLProtected(acl, c.ServiceSID, 0xf01fffff, 0x120000)
}

// supplierTokenProtected applies the helper witness's own predicate to a
// Record or native snapshot. A NULL DACL stays NULL and is refused.
func supplierTokenProtected(s serviceready.SecurityPolicy, serviceSID string) bool {
	t := helperLaunchToken{owner: s.OwnerSID}
	if s.ACL != nil {
		t.acl = make([]helperLaunchACE, 0, len(s.ACL))
	}
	for _, a := range s.ACL {
		t.acl = append(t.acl, helperLaunchACE{kind: a.Type, flags: a.Flags, mask: a.Mask, sid: a.SID})
	}
	return helperLaunchTokenProtected(t, serviceSID)
}

func supplierCloneSecurity(s serviceready.SecurityPolicy) serviceready.SecurityPolicy {
	if s.ACL != nil {
		s.ACL = append(make([]protectedkey.ACE, 0, len(s.ACL)), s.ACL...)
	}
	return s
}

func CloneHelperSupplierConfig(c HelperSupplierConfig) HelperSupplierConfig {
	c.Image.Nodes = append([]protectedkey.NodePolicy(nil), c.Image.Nodes...)
	for i := range c.Image.Nodes {
		if c.Image.Nodes[i].ACL != nil {
			c.Image.Nodes[i].ACL = append(make([]protectedkey.ACE, 0, len(c.Image.Nodes[i].ACL)), c.Image.Nodes[i].ACL...)
		}
	}
	c.ServiceSecurity = supplierCloneSecurity(c.ServiceSecurity)
	c.ProcessSecurity = supplierCloneSecurity(c.ProcessSecurity)
	c.TokenSecurity = supplierCloneSecurity(c.TokenSecurity)
	return c
}

// NewSIDEndpointFactoryWithSupplier retains the original Record's values.
// Acquisition opens only when the helper value compiled into this image equals
// that Record; the unset default keeps RuntimeConfigured false and Prepare
// unconfigured before any lease, Move or native call.
func NewSIDEndpointFactoryWithSupplier(service *bootstrapauth.Factory, serviceToken uintptr, expectedOperatorSID string, policy SIDPolicy, parent bootstrapauth.Profile, config HelperSupplierConfig) (*EndpointFactory, error) {
	if !helperHandle(serviceToken) || ValidateHelperSupplierConfig(parent, policy, config) != nil {
		return nil, ErrUnconfigured
	}
	f, err := NewSIDEndpointFactory(service, expectedOperatorSID, policy)
	if err != nil {
		return nil, err
	}
	f.serviceToken = serviceToken
	c := CloneHelperSupplierConfig(config)
	f.supplier, f.parent = &c, parent
	return f, nil
}

func (f *EndpointFactory) supplierEnabled() bool {
	return f.supplier != nil && supplierAcquisitionOpen(f.parent, *f.supplier)
}

// supplierAcquisitionOpen is the one gate for both registering and acquiring a
// Record's supplier. It rests on the unapproved compiled value because the
// helper child has no approved channel for its expected parent/supplier
// identity, so it stays closed by default.
func supplierAcquisitionOpen(parent bootstrapauth.Profile, c HelperSupplierConfig) bool {
	return protectedHelperLaunch.matchesSupplier(parent, c)
}

// supplierBatchSpec makes a new lease and launch ID for each Batch from the
// same retained Record; no lease, generation or token is shared between
// requests. The lease is bound to its Batch by prepareBatch before any I/O.
func (f *EndpointFactory) supplierBatchSpec(k supplierKernel) (nativeBatchSpec, error) {
	if !f.supplierEnabled() {
		return nativeBatchSpec{}, ErrUnconfigured
	}
	c := *f.supplier
	lease, err := newSupplierLease(f.parent, f.sidPolicy, c, k)
	if err != nil {
		return nativeBatchSpec{}, err
	}
	var id [32]byte
	if _, err = rand.Read(id[:]); err != nil {
		return nativeBatchSpec{}, ErrUnconfigured
	}
	launch := nativeLaunchSpec{imagePath: c.Image.Path, imageSHA256: c.Image.SHA256, launchID: hex.EncodeToString(id[:]), maxImageBytes: c.Image.MaxBytes,
		cleanup: c.Cleanup, processSDDL: c.ChildProcessSDDL, threadSDDL: c.ChildThreadSDDL, helperUserSID: supplierLocalService, helperServiceSID: c.ServiceSID}
	report := reportConfig{ownerSID: f.parent.Keys.ProcessUserSID, serviceSID: f.parent.Keys.ServiceSID, helperSID: c.ServiceSID, guestSID: f.sidPolicy.GuestSID, cleanup: c.Cleanup}
	return nativeBatchSpec{supplier: lease, launch: launch, report: report}, nil
}
