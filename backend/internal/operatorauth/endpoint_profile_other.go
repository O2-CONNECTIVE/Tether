//go:build !windows

package operatorauth

import (
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/bootstrapauth"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/serviceready"
)

func prepareNativeEndpointProfileBatch(nativeBatchSpec, *bootstrapauth.Factory, uintptr, batchOwner, profileProviderPolicy) (*nativeBatch, error) {
	return nil, ErrUnconfigured
}

func prepareNativeSoleInputProfileBatch(nativeBatchSpec, *serviceready.SoleInput, *bootstrapauth.Factory, uintptr, batchOwner, profileProviderPolicy) (*nativeBatch, error) {
	return nil, ErrUnconfigured
}

func prepareNativeSupplierSIDBatch(*EndpointFactory, *serviceready.SoleInput, batchOwner) (*nativeBatch, error) {
	return nil, ErrUnconfigured
}

func prepareNativeSoleInputSIDBatch(nativeBatchSpec, *serviceready.SoleInput, *bootstrapauth.Factory, uintptr, batchOwner, SIDPolicy) (*nativeBatch, error) {
	return nil, ErrUnconfigured
}
