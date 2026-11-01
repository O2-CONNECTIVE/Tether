package servicehost

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/operatorauth"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/protectedkey"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/serviceready"
)

func supplierBindingFixture(t *testing.T) (PreviewServiceConfig, operatorauth.SIDPolicy, operatorauth.HelperSupplierConfig) {
	t.Helper()
	c := previewConfigFixture(t)
	p := &c.Ready.Profile
	old := p.Keys.ProcessUserSID
	p.Keys.ProcessUserSID = "S-1-5-18"
	fixNodes := func(nodes []protectedkey.NodePolicy) {
		for i := range nodes {
			if nodes[i].OwnerSID == old {
				nodes[i].OwnerSID = "S-1-5-18"
			}
			for j := range nodes[i].ACL {
				if nodes[i].ACL[j].SID == old {
					nodes[i].ACL[j].SID = "S-1-5-18"
				}
			}
		}
	}
	fixNodes(p.SupervisorImage.Nodes)
	fixNodes(p.ChildImage.Nodes)
	for _, k := range p.Keys.Catalog {
		fixNodes(k.Nodes)
	}
	c.Ready.ServiceSecurity.OwnerSID = "S-1-5-18"
	c.Ready.ProcessSecurity.OwnerSID = "S-1-5-18"
	policy := operatorauth.SIDPolicy{OperatorSID: p.Keys.OperatorSID, GuestSID: p.Keys.GuestSID}
	security := func(mask uint32) serviceready.SecurityPolicy {
		return serviceready.SecurityPolicy{OwnerSID: "S-1-5-18", Protected: true, ACL: []protectedkey.ACE{{SID: "S-1-5-18", Mask: mask}}}
	}
	const helperSID = "S-1-5-80-6-7-8-9-10"
	prefix := "D:P(A;;GA;;;S-1-5-18)(A;;GA;;;" + p.Keys.ServiceSID + ")"
	helper := operatorauth.HelperSupplierConfig{ServiceName: "SyntheticAuthSupplier", ServiceSID: helperSID, ServiceCommandLine: `"` + p.ChildImage.Path + `" --auth-supplier-internal`, Image: p.ChildImage, ServiceSecurity: security(0xf01ff), ProcessSecurity: security(0x1fffff), TokenSecurity: security(0xf01ff), ChildProcessSDDL: prefix + "(A;;0x120400;;;" + helperSID + ")", ChildThreadSDDL: prefix + "(A;;0x120040;;;" + helperSID + ")", Cleanup: time.Second, RunBudget: time.Second}
	return c, policy, helper
}

func TestSupplierBindingUsesSameUnusedServiceOwnerAndKeepsMissingEntryClosed(t *testing.T) {
	c, policy, helper := supplierBindingFixture(t)
	s, err := NewPreviewService(c)
	if err != nil {
		t.Fatal(err)
	}
	o := &confirmationOwnerModel{service: s}
	if s.BindOperatorSupplier(o, policy, helper) == nil {
		t.Fatal("unbound owner supplied helper")
	}
	if err = s.BindOuterFinalizer(o); err != nil {
		t.Fatal(err)
	}
	if s.BindOperatorSupplier(&confirmationOwnerModel{service: s}, policy, helper) == nil {
		t.Fatal("foreign owner supplied helper")
	}
	bad := helper
	bad.ServiceSID = policy.GuestSID
	if s.BindOperatorSupplier(o, policy, bad) == nil {
		t.Fatal("guest supplied helper principal")
	}
	if s.control.factories.batch != nil || s.auth.sidRequired {
		t.Fatal("rejected configuration partially bound")
	}
	if err = s.BindOperatorSupplier(o, policy, helper); err != nil {
		t.Fatal(err)
	}
	if s.control.factories.batch.RuntimeConfigured() {
		t.Fatal("pure configuration enabled missing native entry")
	}
	if !errors.Is(s.BindOperatorSupplier(o, policy, helper), ErrAlreadyUsed) || !errors.Is(s.BindOperatorSIDPolicy(o, policy), ErrAlreadyUsed) {
		t.Fatal("supplier policy replaced")
	}
	s.control.prepare = func(context.Context) (serviceControlWitness, error) {
		t.Fatal("configuration acquired SCM/pipe")
		return serviceControlWitness{}, ErrProvisioningRejected
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err = s.auth.exchangeControlEndpoint(ctx); !errors.Is(err, ErrProvisioningUnconfigured) {
		t.Fatal("unconfigured child reached runtime", err)
	}
	if err = s.Close(ctx); err != nil {
		t.Fatal(err)
	}
}
