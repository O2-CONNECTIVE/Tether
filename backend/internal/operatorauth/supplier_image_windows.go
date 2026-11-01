//go:build windows

package operatorauth

import (
	"context"
	"reflect"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/bootstrapauth"
	"golang.org/x/sys/windows"
)

// Ancestors and leaf are pinned without write/delete sharing. A leaf hash by
// itself cannot prove a protected image path.
type supplierImage struct {
	config   bootstrapauth.Image
	files    []windows.Handle
	baseline []windows.ByHandleFileInformation
}

func openSupplierImage(ctx context.Context, c bootstrapauth.Image) (out *supplierImage, result error) {
	out = &supplierImage{config: c}
	defer func() {
		if recover() != nil {
			result = ErrUnconfirmed
		}
	}()
	for i, node := range c.Nodes {
		if !finite(ctx) {
			return out, ErrDenied
		}
		path, err := windows.UTF16PtrFromString(node.Path)
		if err != nil {
			return out, ErrDenied
		}
		access := uint32(windows.FILE_READ_ATTRIBUTES | windows.READ_CONTROL)
		flags := uint32(windows.FILE_FLAG_OPEN_REPARSE_POINT | windows.FILE_FLAG_BACKUP_SEMANTICS)
		if i == len(c.Nodes)-1 {
			access = windows.GENERIC_READ | windows.READ_CONTROL
		}
		h, err := windows.CreateFile(path, access, windows.FILE_SHARE_READ, nil, windows.OPEN_EXISTING, flags, 0)
		if helperHandle(uintptr(h)) {
			out.files = append(out.files, h)
		}
		if err != nil || !helperHandle(uintptr(h)) {
			return out, ErrDenied
		}
		info, err := out.inspect(i)
		if err != nil {
			return out, err
		}
		out.baseline = append(out.baseline, info)
	}
	return out, out.check(ctx)
}

func (i *supplierImage) inspect(index int) (windows.ByHandleFileInformation, error) {
	var info windows.ByHandleFileInformation
	h, node := i.files[index], i.config.Nodes[index]
	kind, err := windows.GetFileType(h)
	if err != nil || kind != windows.FILE_TYPE_DISK || windows.GetFileInformationByHandle(h, &info) != nil || info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 || info.FileIndexHigh == 0 && info.FileIndexLow == 0 || uint64(info.VolumeSerialNumber) != i.config.Volume.Serial {
		return info, ErrDenied
	}
	leaf := index == len(i.config.Nodes)-1
	if (info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY == 0) != leaf || leaf && info.NumberOfLinks != 1 {
		return info, ErrDenied
	}
	var path [32768]uint16
	n, err := windows.GetFinalPathNameByHandle(h, &path[0], uint32(len(path)), 0)
	if err != nil || n == 0 || n >= uint32(len(path)) || windows.UTF16ToString(path[:n]) != `\\?\`+node.Path {
		return info, ErrDenied
	}
	var fs [32]uint16
	var serial, max, flags uint32
	if windows.GetVolumeInformationByHandle(h, nil, 0, &serial, &max, &flags, &fs[0], uint32(len(fs))) != nil || uint64(serial) != i.config.Volume.Serial || windows.UTF16ToString(fs[:]) != i.config.Volume.Filesystem {
		return info, ErrDenied
	}
	security, err := supplierObjectSecurity(h, windows.SE_FILE_OBJECT)
	if err != nil || security.OwnerSID != node.OwnerSID || !reflect.DeepEqual(security.ACL, node.ACL) {
		return info, ErrDenied
	}
	// Read access can update access time; it is not file identity or mutability.
	info.LastAccessTime = windows.Filetime{}
	return info, nil
}

func (i *supplierImage) check(ctx context.Context) error {
	if i == nil || len(i.files) != len(i.config.Nodes) || len(i.baseline) != len(i.files) || len(i.files) == 0 {
		return ErrDenied
	}
	for n := range i.files {
		if !finite(ctx) {
			return ErrDenied
		}
		info, err := i.inspect(n)
		if err != nil || info != i.baseline[n] {
			return ErrDenied
		}
	}
	s := nativeLaunchSpec{imagePath: i.config.Path, imageSHA256: i.config.SHA256, maxImageBytes: i.config.MaxBytes}
	return launchCheckImage(ctx, uintptr(i.files[len(i.files)-1]), s)
}

func (i *supplierImage) close(ctx context.Context) error {
	for n := len(i.files) - 1; n >= 0; n-- {
		if i.files[n] != 0 {
			if !finite(ctx) || windows.CloseHandle(i.files[n]) != nil {
				return ErrUnconfirmed
			}
			i.files[n] = 0
		}
	}
	return nil
}
