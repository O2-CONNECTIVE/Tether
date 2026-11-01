//go:build !windows

package operatorauth

import "github.com/O2-CONNECTIVE/rescue-host/backend/internal/bootstrapauth"

func closeNativeHelperEntryHandle(uintptr) error { return ErrUnconfigured }

// No SCM supplier dispatcher exists outside Windows.
func nativeSupplierEntry() supplierEntryKernel { return nil }

// No SCM supplier registrar exists outside Windows.
func nativeSupplierRegistrar(bootstrapauth.Profile) supplierRegistrar { return nil }
