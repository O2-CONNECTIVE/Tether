//go:build windows

package operatorauth

import (
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/bootstrapauth"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/serviceready"
)

// 正規policy発行元とService adapterは未接続。候補とは独立に供給したpolicyと、
// same exchangeの破壊的Moveが返したread所有参照が必要である。
// pure prepareだけを行い、実取得はService登録後の同worker Startに限定する。
func prepareNativeEndpointProfileBatch(s nativeBatchSpec, factory *bootstrapauth.Factory, serviceToken uintptr, owner batchOwner, policy profileProviderPolicy) (*nativeBatch, error) {
	s, err := endpointProfileSpec(s, policy, &nativeEndpointCandidateKernel{})
	if err != nil {
		return nil, err
	}
	return prepareNativeBatch(s, factory, serviceToken, owner)
}

// 新しい typed 経路は raw input を受けず、同交換の具体 source を pure 保持する。
// 公開 factory 登録・same Service/profile 照合・正規 policy 発行元は未接続。
func prepareNativeSoleInputProfileBatch(s nativeBatchSpec, input *serviceready.SoleInput, factory *bootstrapauth.Factory, serviceToken uintptr, owner batchOwner, policy profileProviderPolicy) (*nativeBatch, error) {
	s, err := soleInputProfileSpec(s, input, policy, &nativeEndpointCandidateKernel{})
	if err != nil {
		return nil, err
	}
	return prepareNativeBatch(s, factory, serviceToken, owner)
}

// Supplier SID mode composes one native lease per request from the retained
// Record and then uses the same retained Batch and native Move. Pure prepare
// only: the supplier starts inside the registered Batch's Start.
func prepareNativeSupplierSIDBatch(f *EndpointFactory, input *serviceready.SoleInput, owner batchOwner) (*nativeBatch, error) {
	s, err := f.supplierBatchSpec(nativeSupplierKernel{parent: f.parent})
	if err != nil {
		return nil, err
	}
	return prepareNativeSoleInputSIDBatch(s, input, f.service, f.serviceToken, owner, f.sidPolicy)
}

// SID mode uses the same retained Batch and native Move.
func prepareNativeSoleInputSIDBatch(s nativeBatchSpec, input *serviceready.SoleInput, factory *bootstrapauth.Factory, serviceToken uintptr, owner batchOwner, policy SIDPolicy) (*nativeBatch, error) {
	s, err := soleInputSIDSpec(s, input, policy)
	if err != nil {
		return nil, err
	}
	return prepareNativeBatch(s, factory, serviceToken, owner)
}
