package agentkeys

import (
	"encoding/base64"
	"strings"
	"unicode/utf8"
)

const (
	maxPathBytes      = 1024
	maxComponentBytes = 255
	maxComponents     = 32
)

// pathStyle selects the platform's canonical path syntax and how a held handle
// is bound to its name.
type pathStyle uint8

const (
	// posixPaths: "/a/b/key". Names are bound by re-resolving each component
	// relative to the held parent handle (fstatat without following links).
	posixPaths pathStyle = iota + 1
	// drivePaths: "C:\a\b\key". Names are bound by the final path reported by
	// the held handle, and every node must be on the root's volume.
	drivePaths
)

func (s pathStyle) finalPath() bool  { return s == drivePaths }
func (s pathStyle) sameVolume() bool { return s == drivePaths }

type role uint8

const (
	roleAncestor role = iota + 1 // the root and directories above the key's parent
	roleParent                   // the directory that directly contains the key
	roleLeaf                     // the key file
)

// node is one path component to open in order from the root.
type node struct {
	path string // canonical path of this node
	name string // component name in its parent; "" for the root
	role role
}

// nodes validates path and returns the root followed by each component. The
// key must have at least one parent below the root.
func (s pathStyle) nodes(path string) ([]node, bool) {
	var root, sep string
	switch s {
	case posixPaths:
		root, sep = "/", "/"
		if !strings.HasPrefix(path, "/") {
			return nil, false
		}
	case drivePaths:
		sep = `\`
		if len(path) < 3 || path[0] < 'A' || path[0] > 'Z' || path[1:3] != `:\` {
			return nil, false
		}
		root = path[:3]
	default:
		return nil, false
	}
	if len(path) > maxPathBytes || !utf8.ValidString(path) {
		return nil, false
	}
	parts := strings.Split(path[len(root):], sep)
	if len(parts) < 2 || len(parts) > maxComponents {
		return nil, false
	}
	nodes := make([]node, 0, len(parts)+1)
	nodes = append(nodes, node{path: root, role: roleAncestor})
	current := root
	for i, part := range parts {
		if !validComponent(s, part) {
			return nil, false
		}
		if i > 0 {
			current += sep
		}
		current += part
		r := roleAncestor
		switch i {
		case len(parts) - 1:
			r = roleLeaf
		case len(parts) - 2:
			r = roleParent
		}
		nodes = append(nodes, node{path: current, name: part, role: r})
	}
	return nodes, true
}

func validComponent(s pathStyle, part string) bool {
	if part == "" || part == "." || part == ".." || len(part) > maxComponentBytes {
		return false
	}
	for _, c := range part {
		if c < 0x20 || c == 0x7f || c == utf8.RuneError {
			return false
		}
		if s == posixPaths && c == '/' {
			return false
		}
		if s == drivePaths && strings.ContainsRune(`\/:<>"|?*`, c) {
			return false
		}
	}
	if s != drivePaths {
		return true
	}
	// Win32 silently strips trailing dots/spaces and maps device names, so a
	// configured name could differ from the object actually opened.
	if strings.HasSuffix(part, ".") || strings.HasSuffix(part, " ") {
		return false
	}
	stem := strings.ToUpper(strings.SplitN(part, ".", 2)[0])
	switch stem {
	case "CON", "PRN", "AUX", "NUL", "CONIN$", "CONOUT$":
		return false
	}
	for _, device := range [...]string{"COM", "LPT"} {
		if digit, ok := strings.CutPrefix(stem, device); ok {
			switch digit { // Win32 also reserves the superscript digits ¹ ² ³
			case "0", "1", "2", "3", "4", "5", "6", "7", "8", "9", "¹", "²", "³":
				return false
			}
		}
	}
	return true
}

func validFingerprint(s string) bool {
	if !strings.HasPrefix(s, "SHA256:") {
		return false
	}
	b, err := base64.RawStdEncoding.DecodeString(strings.TrimPrefix(s, "SHA256:"))
	return err == nil && len(b) == 32 && "SHA256:"+base64.RawStdEncoding.EncodeToString(b) == s
}
