//go:build !windows && (!linux || android) && !(darwin && cgo)

package agentkeys

// Other platforms (including android, which also satisfies the linux build
// tag), and Darwin builds without cgo, have no implemented inspection of
// effective file ACLs, so they refuse after validating the configuration. On
// Darwin the missing piece is acl_get_fd_np(3), a libSystem function this
// package reaches only through cgo.
const nativeStyle = posixPaths

func nativeKernel() (kernel, error) { return nil, ErrUnsupported }
