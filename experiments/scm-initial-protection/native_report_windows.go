//go:build windows

package main

import (
	"encoding/binary"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

type nativeReportWriter struct{ serviceSID *string }

// The controller precreates this empty file with a trusted owner and exact
// protected DACL. The probe neither creates paths nor changes their security.
func (w nativeReportWriter) WriteReport(data []byte) (failures []Failure) {
	if len(data) == 0 || len(data) > maxReportBytes || w.serviceSID == nil || *w.serviceSID == "" {
		return invalid("report_precondition")
	}
	var held []windows.Handle
	defer func() {
		for i := len(held) - 1; i >= 0; i-- {
			failures = append(failures, failure("report_close", windows.CloseHandle(held[i]))...)
		}
	}()
	for i, directory := range []string{`C:\`, `C:\ProgramData`, `C:\ProgramData\RescueHost-ProtectionProbe-20261030`, `C:\ProgramData\RescueHost-ProtectionProbe-20261030\reports`} {
		access := uint32(windows.FILE_READ_ATTRIBUTES)
		share := uint32(windows.FILE_SHARE_READ | windows.FILE_SHARE_WRITE)
		if i >= 2 {
			access |= windows.READ_CONTROL
			share = windows.FILE_SHARE_READ
		}
		h, err := openFixedPath(directory, access, share, windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT)
		if err != nil {
			return failure("report_open_ancestor", err)
		}
		held = append(held, h)
		if f := verifyPath(h, directory, true); len(f) != 0 {
			return f
		}
		if i >= 2 {
			if f := verifyReportACL(h, *w.serviceSID, 0x1200a9); len(f) != 0 {
				return f
			}
		}
	}
	// FlushFileBuffers requires GENERIC_WRITE; the dedicated service ACE below
	// uses its file-specific mapping without DELETE, WRITE_DAC, or WRITE_OWNER.
	h, err := openFixedPath(reportPath, windows.GENERIC_WRITE, 0, windows.FILE_FLAG_OPEN_REPARSE_POINT|windows.FILE_FLAG_WRITE_THROUGH)
	if err != nil {
		return failure("report_open_existing", err)
	}
	held = append(held, h)
	if f := verifyPath(h, reportPath, false); len(f) != 0 {
		return f
	}
	if f := verifyReportACL(h, *w.serviceSID, 0x120116); len(f) != 0 {
		return f
	}
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(h, &info); err != nil {
		return failure("report_file_information", err)
	}
	if info.FileSizeHigh != 0 || info.FileSizeLow != 0 || info.NumberOfLinks != 1 {
		return invalid("report_not_empty_single_file")
	}
	var written uint32
	if err := windows.WriteFile(h, data, &written, nil); err != nil {
		return failure("report_write", err)
	}
	if written != uint32(len(data)) {
		return invalid("report_short_write")
	}
	if err := windows.FlushFileBuffers(h); err != nil {
		return failure("report_flush", err)
	}
	return nil
}

func openFixedPath(path string, access, share, flags uint32) (windows.Handle, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}
	return windows.CreateFile(name, access, share, nil, windows.OPEN_EXISTING, flags, 0)
}

func verifyPath(h windows.Handle, expected string, directory bool) []Failure {
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(h, &info); err != nil {
		return failure("report_path_information", err)
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 || (info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0) != directory {
		return invalid("report_unsafe_path")
	}
	kind, err := windows.GetFileType(h)
	if err != nil {
		return failure("report_path_type", err)
	}
	if kind != windows.FILE_TYPE_DISK {
		return invalid("report_not_disk")
	}
	var path [512]uint16
	size, err := windows.GetFinalPathNameByHandle(h, &path[0], uint32(len(path)), 0)
	if err != nil {
		return failure("report_final_path", err)
	}
	if size == 0 || size >= uint32(len(path)) {
		return invalid("report_final_path_bound")
	}
	actual := windows.UTF16ToString(path[:size])
	if !strings.EqualFold(strings.TrimSuffix(actual, `\`), strings.TrimSuffix(`\\?\`+expected, `\`)) {
		return invalid("report_final_path_mismatch")
	}
	return nil
}

func verifyReportACL(h windows.Handle, expectedServiceSID string, serviceMask uint32) []Failure {
	return withSecurity(h, windows.SE_FILE_OBJECT, "report_security", func(sd *windows.SECURITY_DESCRIPTOR, memory []byte) []Failure {
		owner, defaulted, err := sd.Owner()
		if err != nil {
			return failure("report_owner", err)
		}
		ownerSID, ok := sidInBuffer(memory, owner)
		if !ok || defaulted || ownerSID != "S-1-5-32-544" {
			return invalid("report_untrusted_owner")
		}
		control, _, err := sd.Control()
		if err != nil {
			return failure("report_control", err)
		}
		if control&windows.SE_DACL_PROTECTED == 0 {
			return invalid("report_unprotected_dacl")
		}
		acl, defaulted, err := sd.DACL()
		if err != nil {
			return failure("report_dacl", err)
		}
		aclBytes, ok := aclInBuffer(memory, acl)
		if !ok || defaulted || acl.AceCount != 3 {
			return invalid("report_dacl_shape")
		}
		expected := map[string]uint32{"S-1-5-32-544": 0x1f01ff, "S-1-5-18": 0x1f01ff, expectedServiceSID: serviceMask}
		for i := uint32(0); i < 3; i++ {
			var ace *windows.ACCESS_ALLOWED_ACE
			if err := windows.GetAce(acl, i, &ace); err != nil {
				return failure("report_ace", err)
			}
			if !contained(aclBytes, unsafe.Pointer(ace), 8) {
				return invalid("report_ace_bound")
			}
			prefix := unsafe.Slice((*byte)(unsafe.Pointer(ace)), 8)
			size := uintptr(binary.LittleEndian.Uint16(prefix[2:]))
			if prefix[0] != windows.ACCESS_ALLOWED_ACE_TYPE || prefix[1] != 0 || size < 16 || !contained(aclBytes, unsafe.Pointer(ace), size) {
				return invalid("report_ace_shape")
			}
			sid, ok := sidInBuffer(unsafe.Slice((*byte)(unsafe.Pointer(ace)), int(size)), (*windows.SID)(unsafe.Pointer(&ace.SidStart)))
			mask, exists := expected[sid]
			if !ok || !exists || uint32(ace.Mask) != mask {
				return invalid("report_ace_identity_or_mask")
			}
			delete(expected, sid)
		}
		if len(expected) != 0 {
			return invalid("report_missing_ace")
		}
		return nil
	})
}
