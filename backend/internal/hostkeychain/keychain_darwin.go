//go:build darwin && cgo

package hostkeychain

/*
#cgo darwin LDFLAGS: -framework Security -framework CoreFoundation
#include <CoreFoundation/CoreFoundation.h>
#include <Security/Security.h>
#include <stdint.h>
#include <stdlib.h>
#include <string.h>

enum { tetherErrSecItemNotFound = -25300, tetherErrSecDuplicateItem = -25299 };

static CFMutableDictionaryRef tetherQuery(const char *account, Boolean useDataProtection) {
	CFMutableDictionaryRef query = CFDictionaryCreateMutable(kCFAllocatorDefault, 0,
		&kCFTypeDictionaryKeyCallBacks, &kCFTypeDictionaryValueCallBacks);
	CFStringRef service = CFStringCreateWithCString(kCFAllocatorDefault,
		"com.o2connective.tether.host-key", kCFStringEncodingUTF8);
	CFStringRef itemAccount = CFStringCreateWithCString(kCFAllocatorDefault, account,
		kCFStringEncodingUTF8);
	if (query == NULL || service == NULL || itemAccount == NULL) {
		if (query != NULL) CFRelease(query);
		if (service != NULL) CFRelease(service);
		if (itemAccount != NULL) CFRelease(itemAccount);
		return NULL;
	}
	CFDictionarySetValue(query, kSecClass, kSecClassGenericPassword);
	CFDictionarySetValue(query, kSecAttrService, service);
	CFDictionarySetValue(query, kSecAttrAccount, itemAccount);
	CFDictionarySetValue(query, kSecAttrSynchronizable, kCFBooleanFalse);
	if (useDataProtection) {
		CFDictionarySetValue(query, kSecUseDataProtectionKeychain, kCFBooleanTrue);
	}
	CFRelease(service);
	CFRelease(itemAccount);
	return query;
}

static OSStatus tetherStore(const char *account, const uint8_t *secret, size_t length,
		Boolean useDataProtection) {
	CFMutableDictionaryRef query = tetherQuery(account, useDataProtection);
	if (query == NULL) return errSecAllocate;
	CFMutableDataRef value = CFDataCreateMutable(kCFAllocatorDefault, (CFIndex)length);
	if (value == NULL) {
		CFRelease(query);
		return errSecAllocate;
	}
	CFDataAppendBytes(value, secret, (CFIndex)length);
	CFDictionarySetValue(query, kSecAttrAccessible, kSecAttrAccessibleAfterFirstUnlockThisDeviceOnly);
	CFDictionarySetValue(query, kSecValueData, value);
	OSStatus status = SecItemAdd(query, NULL);
	UInt8 *bytes = CFDataGetMutableBytePtr(value);
	if (bytes != NULL) {
		volatile UInt8 *wipe = bytes;
		for (CFIndex i = 0; i < CFDataGetLength(value); i++) wipe[i] = 0;
	}
	CFRelease(value);
	CFRelease(query);
	return status;
}

static OSStatus tetherLoad(const char *account, Boolean useDataProtection,
		UInt8 **secret, size_t *length) {
	CFMutableDictionaryRef query = tetherQuery(account, useDataProtection);
	if (query == NULL) return errSecAllocate;
	CFDictionarySetValue(query, kSecReturnData, kCFBooleanTrue);
	CFDictionarySetValue(query, kSecMatchLimit, kSecMatchLimitOne);
	CFTypeRef result = NULL;
	OSStatus status = SecItemCopyMatching(query, &result);
	if (status == errSecSuccess) {
		if (result == NULL || CFGetTypeID(result) != CFDataGetTypeID()) {
			status = errSecDecode;
		} else {
			CFDataRef data = (CFDataRef)result;
			CFIndex count = CFDataGetLength(data);
			if (count < 0 || (uint64_t)count > (uint64_t)SIZE_MAX) {
				status = errSecDecode;
			} else {
				UInt8 *copy = (UInt8 *)malloc(count == 0 ? 1 : (size_t)count);
				if (copy == NULL) {
					status = errSecAllocate;
				} else {
					if (count > 0) memcpy(copy, CFDataGetBytePtr(data), (size_t)count);
					*secret = copy;
					*length = (size_t)count;
				}
				}
			}
		}
	if (result != NULL) CFRelease(result);
	CFRelease(query);
	return status;
}

static OSStatus tetherDelete(const char *account, Boolean useDataProtection) {
	CFMutableDictionaryRef query = tetherQuery(account, useDataProtection);
	if (query == NULL) return errSecAllocate;
	OSStatus status = SecItemDelete(query);
	CFRelease(query);
	return status;
}

static void tetherClear(UInt8 *secret, size_t length) {
	if (secret == NULL) return;
	volatile UInt8 *wipe = secret;
	for (size_t i = 0; i < length; i++) wipe[i] = 0;
	free(secret);
}
*/
import "C"

import (
	"fmt"
	"unsafe"
)

func storeSeed(fingerprint string, seed []byte) error {
	account := C.CString(fingerprint)
	defer C.free(unsafe.Pointer(account))
	secret := C.CBytes(seed)
	defer C.tetherClear((*C.UInt8)(secret), C.size_t(len(seed)))
	status := C.tetherStore(account, (*C.uint8_t)(secret), C.size_t(len(seed)), dataProtectionFlag())
	return keychainStatus("store", status)
}

func loadSeed(fingerprint string) ([]byte, error) {
	account := C.CString(fingerprint)
	defer C.free(unsafe.Pointer(account))
	var secret *C.UInt8
	var length C.size_t
	status := C.tetherLoad(account, dataProtectionFlag(), &secret, &length)
	if err := keychainStatus("load", status); err != nil {
		return nil, err
	}
	defer C.tetherClear(secret, length)
	if length != 32 {
		return nil, ErrInvalid
	}
	return C.GoBytes(unsafe.Pointer(secret), C.int(length)), nil
}

func deleteSeed(fingerprint string) error {
	account := C.CString(fingerprint)
	defer C.free(unsafe.Pointer(account))
	status := C.tetherDelete(account, dataProtectionFlag())
	if status == C.tetherErrSecItemNotFound {
		return nil
	}
	return keychainStatus("delete", status)
}

func dataProtectionFlag() C.Boolean {
	if UseDataProtectionKeychain {
		return C.Boolean(1)
	}
	return C.Boolean(0)
}

func keychainStatus(operation string, status C.OSStatus) error {
	if status == 0 {
		return nil
	}
	if status == C.tetherErrSecItemNotFound {
		return ErrNotFound
	}
	if status == C.tetherErrSecDuplicateItem {
		return ErrDuplicate
	}
	return fmt.Errorf("%w: %s failed (OSStatus %d)", ErrUnavailable, operation, int32(status))
}
