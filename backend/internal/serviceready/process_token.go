package serviceready

import (
	"reflect"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/bootstrapauth"
)

// processTokenState describes the actual token object, not TokenDefaultDacl.
// Values only cross the private kernel seam; public callers cannot supply proof.
type processTokenState struct {
	id, modified, currentID, currentModified, borrowedID, borrowedModified uint64
	security                                                               SecurityPolicy
}

func processQueryExposed(profile bootstrapauth.Profile, policy SecurityPolicy) bool {
	for _, ace := range policy.ACL {
		if ace.Type == 0 && !trusted(profile, ace.SID) && ace.Mask&0x1400 != 0 {
			return true
		}
	}
	return false
}

func validProcessToken(profile bootstrapauth.Profile, token processTokenState) bool {
	if token.id == 0 || token.modified == 0 || token.id != token.currentID || token.id != token.borrowedID ||
		token.modified != token.currentModified || token.modified != token.borrowedModified {
		return false
	}
	s := token.security
	if !trusted(profile, s.OwnerSID) || !validSID(s.OwnerSID) || s.ACL == nil || len(s.ACL) > 128 {
		return false
	}
	for _, ace := range s.ACL {
		// Token objects do not support SYNCHRONIZE. Generic/MAXIMUM_ALLOWED and
		// unsupported ACEs fail closed even when a separate DENY looks protective.
		if ace.Type > 1 || ace.Flags != 0 || ace.Mask == 0 || ace.Mask & ^uint32(0x010f01ff) != 0 || !validSID(ace.SID) {
			return false
		}
		if ace.Type == 0 && !trusted(profile, ace.SID) && ace.Mask & ^uint32(0x20008) != 0 {
			return false
		}
	}
	return true
}

func (p *Plane) verifyProcessToken() error {
	observed, err := p.process.inspectToken(p.c.PrimaryToken)
	if err != nil {
		return err
	}
	if !validProcessToken(p.c.Profile, observed) {
		return ErrDenied
	}
	if p.tokenEvidence == nil {
		observed.security = cloneSecurity(observed.security)
		p.tokenEvidence = &observed
		return nil
	}
	prior := p.tokenEvidence
	// Pin the whole observed descriptor, including its protection bit. A new
	// inheritance-protection setting is not inferred from safe token rights.
	if prior.id != observed.id || prior.modified != observed.modified || !reflect.DeepEqual(observed.security, prior.security) {
		return ErrDenied
	}
	return nil
}
