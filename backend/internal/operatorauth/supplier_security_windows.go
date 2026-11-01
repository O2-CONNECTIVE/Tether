//go:build windows

package operatorauth

import (
	"runtime"
	"strconv"
	"unsafe"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/protectedkey"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/serviceready"
	"golang.org/x/sys/windows"
)

// supplierSetServiceSecurity writes the Record's exact owner and DACL to the
// registration this owner created. The caller re-reads and compares the result;
// a value that SCM stores differently is refused, not adopted.
func supplierSetServiceSecurity(service windows.Handle, s serviceready.SecurityPolicy) error {
	sddl := "O:" + s.OwnerSID + "D:"
	if s.Protected {
		sddl += "P"
	}
	for _, a := range s.ACL {
		if a.Type > 1 || a.Flags != 0 || !reportSIDValid(a.SID) {
			return ErrDenied
		}
		kind := "A"
		if a.Type == 1 {
			kind = "D"
		}
		sddl += "(" + kind + ";;0x" + strconv.FormatUint(uint64(a.Mask), 16) + ";;;" + a.SID + ")"
	}
	sd, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil || sd == nil || !sd.IsValid() {
		return ErrDenied
	}
	defer runtime.KeepAlive(sd)
	owner, _, err := sd.Owner()
	if err != nil || owner == nil || !owner.IsValid() {
		return ErrDenied
	}
	dacl, _, err := sd.DACL()
	if err != nil || dacl == nil {
		return ErrDenied
	}
	info := windows.SECURITY_INFORMATION(windows.OWNER_SECURITY_INFORMATION | windows.DACL_SECURITY_INFORMATION)
	if s.Protected {
		info |= windows.PROTECTED_DACL_SECURITY_INFORMATION
	} else {
		info |= windows.UNPROTECTED_DACL_SECURITY_INFORMATION
	}
	if windows.SetSecurityInfo(service, windows.SE_SERVICE, info, owner, nil, dacl, nil) != nil {
		return ErrDenied
	}
	return nil
}

func supplierObjectSecurity(handle windows.Handle, kind windows.SE_OBJECT_TYPE) (serviceready.SecurityPolicy, error) {
	sd, err := windows.GetSecurityInfo(handle, kind, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil || sd == nil || !sd.IsValid() {
		return serviceready.SecurityPolicy{}, ErrDenied
	}
	defer runtime.KeepAlive(sd)
	owner, defaulted, err := sd.Owner()
	if err != nil || owner == nil || !owner.IsValid() || defaulted {
		return serviceready.SecurityPolicy{}, ErrDenied
	}
	control, _, err := sd.Control()
	if err != nil || control&windows.SE_DACL_PRESENT == 0 {
		return serviceready.SecurityPolicy{}, ErrDenied
	}
	acl, defaulted, err := sd.DACL()
	if err != nil || defaulted {
		return serviceready.SecurityPolicy{}, ErrDenied
	}
	aces, err := supplierCopyACL(acl)
	if err != nil {
		return serviceready.SecurityPolicy{}, err
	}
	return serviceready.SecurityPolicy{OwnerSID: owner.String(), Protected: control&windows.SE_DACL_PROTECTED != 0, ACL: aces}, nil
}

func supplierCopyACL(acl *windows.ACL) ([]protectedkey.ACE, error) {
	if acl == nil || acl.AceCount > 128 {
		return nil, ErrDenied
	}
	base := uintptr(unsafe.Pointer(acl))
	size := uintptr(*(*uint16)(unsafe.Add(unsafe.Pointer(acl), 2)))
	if size < 8 || size > 64*1024 {
		return nil, ErrDenied
	}
	result := make([]protectedkey.ACE, 0, acl.AceCount)
	for i := uint32(0); i < uint32(acl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if windows.GetAce(acl, i, &ace) != nil || ace == nil {
			return nil, ErrDenied
		}
		address := uintptr(unsafe.Pointer(ace))
		if address < base+8 || address > base+size-16 || ace.Header.AceType > 1 || ace.Header.AceSize < 16 || uintptr(ace.Header.AceSize) > base+size-address {
			return nil, ErrDenied
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		if !sid.IsValid() || sid.Len() != int(ace.Header.AceSize)-8 {
			return nil, ErrDenied
		}
		result = append(result, protectedkey.ACE{Type: ace.Header.AceType, Flags: ace.Header.AceFlags, Mask: uint32(ace.Mask), SID: sid.String()})
	}
	return result, nil
}

// This is inspected separately from the actual token object's DACL. It cannot
// justify object access, and a NULL/unsupported default ACL fails closed. The
// ACEs are judged by the pure supplierDefaultACLProtected.
func supplierDefaultACL(token windows.Token, c HelperSupplierConfig) error {
	var storage [8192]uint64
	var pins runtime.Pinner
	pins.Pin(&storage[0])
	defer pins.Unpin()
	data := unsafe.Slice((*byte)(unsafe.Pointer(&storage[0])), 64*1024)
	var returned uint32
	if windows.GetTokenInformation(token, windows.TokenDefaultDacl, &data[0], uint32(len(data)), &returned) != nil || returned < uint32(unsafe.Sizeof(uintptr(0)))+8 || returned > uint32(len(data)) {
		return ErrDenied
	}
	acl := *(**windows.ACL)(unsafe.Pointer(&data[0]))
	base, address := uintptr(unsafe.Pointer(&data[0])), uintptr(unsafe.Pointer(acl))
	if acl == nil || address < base+unsafe.Sizeof(uintptr(0)) || address%4 != 0 || address > base+uintptr(returned)-8 {
		return ErrDenied
	}
	size := uintptr(*(*uint16)(unsafe.Add(unsafe.Pointer(acl), 2)))
	if size < 8 || size > base+uintptr(returned)-address {
		return ErrDenied
	}
	aces, err := supplierCopyACL(acl)
	if err != nil {
		return err
	}
	if !supplierDefaultACLProtected(aces, c) {
		return ErrDenied
	}
	return nil
}
