// Package serviceprovision は公開配置値の純粋な構造を検証する。
// rescue-service-provisioning/1 は固定source用候補で、未決のoperator/UI wireとは別である。
// Decode/Validate の成功は保護配置元、digest照合、認証、ready、実行許可の証明ではない。
package serviceprovision

import (
	"errors"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/bootstrap"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/bootstrapauth"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/operatorauth"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/serviceready"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/stdio"
	"github.com/O2-CONNECTIVE/rescue-host/backend/servicehost"
)

const Protocol = "rescue-service-provisioning/1"
const RecordBytes = 256 * 1024
const RecordDepth = 16

var ErrRecord = errors.New("serviceの公開配置記録を受理できません")

// Record は非秘密の固定配置値だけを持ち、token/handle/grant/承認/identityを持たない。
// 外部の保護されたdigestとの照合や実token取得は、この型の検証とは別の境界である。
type Record struct {
	Protocol                                            string
	Profile                                             bootstrapauth.Profile
	Endpoint, ServiceCommandLine                        string
	ServiceSecurity, ProcessSecurity                    serviceready.SecurityPolicy
	ReadyCleanupTimeout                                 time.Duration
	Budgets                                             servicehost.Budgets
	PolicyRef                                           string
	PollInterval, OperationTimeout, StdioCleanupTimeout time.Duration
	StderrBytes                                         uint64
	Backend                                             bootstrap.Config
	HostKeyRef                                          string
	Limits                                              stdio.Limits
	// nil preserves the existing unconfigured helper path. Values are protected
	// only after the original Root verifies the complete record and its digest.
	HelperSupplier *operatorauth.HelperSupplierConfig
}

// ValidateRecord は値を検査するだけで、OS APIを呼ばず、tokenの代用品を補わない。
func ValidateRecord(r Record) error {
	if r.Protocol != Protocol {
		return ErrRecord
	}
	if _, err := bootstrapauth.NewNativeFactory(r.Profile); err != nil {
		return ErrRecord
	}
	if !validValues(r) {
		return ErrRecord
	}
	if r.HelperSupplier != nil && operatorauth.ValidateHelperSupplierConfig(r.Profile, operatorauth.SIDPolicy{OperatorSID: r.Profile.Keys.OperatorSID, GuestSID: r.Profile.Keys.GuestSID}, *r.HelperSupplier) != nil {
		return ErrRecord
	}
	return nil
}
