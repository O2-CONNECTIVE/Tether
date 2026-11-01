package operatorauth

import "github.com/O2-CONNECTIVE/rescue-host/backend/internal/bootstrapauth"

// SIDPolicy は方式Bの固定保護入力であり、値の構築自体は認証を行わない。
// AuthenticationId/sessionは実message認証の後だけ接続へ結び付ける。
type SIDPolicy struct {
	OperatorSID, GuestSID string
}

func (p SIDPolicy) valid() bool {
	return reportSIDValid(p.OperatorSID) && reportSIDValid(p.GuestSID) && p.OperatorSID != p.GuestSID
}
func (p SIDPolicy) accepts(actual tokenProjection) bool {
	return p.valid() && actual.user == p.OperatorSID && actual.authentication != 0 && actual.integrity != 0 && actual.kind == 2 && (actual.level == 1 || actual.level == 2) && !actual.restricted && !actual.appContainer
}

// NewSIDEndpointFactory は静的な保護policyだけを複製する。
// このpure構築自体はRoot由来の保護やcallerの認証を証明しない。
// 正規helper token/runtime sourceは未実装であり、Service tokenで代用しない。
func NewSIDEndpointFactory(service *bootstrapauth.Factory, expectedOperatorSID string, policy SIDPolicy) (*EndpointFactory, error) {
	if service == nil || !policy.valid() || policy.OperatorSID != expectedOperatorSID {
		return nil, ErrUnconfigured
	}
	f := &EndpointFactory{service: service, sidPolicy: policy}
	f.self = f
	return f, nil
}
func (f *EndpointFactory) MatchesOperatorSID(sid string) bool {
	return f != nil && f.self == f && f.sidPolicy.valid() && sid == f.sidPolicy.OperatorSID
}

// MatchesSIDPolicy は保護構成の同一性だけを比較し、認証を証明しない。
func (f *EndpointFactory) MatchesSIDPolicy(operatorSID, guestSID string) bool {
	return f.MatchesOperatorSID(operatorSID) && f.sidPolicy.GuestSID == guestSID
}

// RuntimeConfigured は静的policyとは別の正規helper資源供給の有無である。
// 方式Bは元Recordのsupplierと、image内の固定値の一致がある場合だけ真で、
// 既定の未設定imageでは取得を開始できない。実Windowsでの成立は未検証。
func (f *EndpointFactory) RuntimeConfigured() bool {
	if f == nil || f.self != f {
		return false
	}
	if f.sidPolicy != (SIDPolicy{}) {
		return f.supplierEnabled()
	}
	return f.spec.producer != nil
}
