package serviceprovision

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/bootstrapauth"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/protectedkey"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/serviceready"
)

// UntrustedClientProjection is a deterministic packaging candidate, not a
// protected read, authenticated peer, approval or native-client capability.
// Every field is derived from one exact record and the selected service build.
type UntrustedClientProjection struct {
	Protocol           string                   `json:"protocol"`
	Activation         string                   `json:"activation"`
	Trust              string                   `json:"trust"`
	SourceProtocol     string                   `json:"sourceProtocol"`
	RecordSHA256       string                   `json:"recordSHA256"`
	ServiceBuildSHA256 string                   `json:"serviceBuildSHA256"`
	ServiceName        string                   `json:"serviceName"`
	Endpoint           string                   `json:"endpoint"`
	ServiceCommandLine string                   `json:"serviceCommandLine"`
	ProcessUserSID     string                   `json:"processUserSID"`
	ServiceSID         string                   `json:"serviceSID"`
	OperatorSID        string                   `json:"operatorSID"`
	ExcludedGuestSID   string                   `json:"excludedGuestSID"`
	Volume             clientProjectionVolume   `json:"volume"`
	ServiceImage       clientProjectionImage    `json:"serviceImage"`
	ServiceSecurity    clientProjectionSecurity `json:"serviceSecurityExpectation"`
	ProcessSecurity    clientProjectionSecurity `json:"processSecurityExpectation"`
}

type clientProjectionVolume struct {
	Root       string `json:"root"`
	Filesystem string `json:"filesystem"`
	Serial     uint64 `json:"serial,string"`
}
type clientProjectionACE struct {
	Type  uint8  `json:"type"`
	Flags uint8  `json:"flags"`
	Mask  uint32 `json:"mask"`
	SID   string `json:"sid"`
}
type clientProjectionNode struct {
	Path     string                `json:"path"`
	OwnerSID string                `json:"ownerSID"`
	ACL      []clientProjectionACE `json:"acl"`
}
type clientProjectionSecurity struct {
	OwnerSID  string                `json:"ownerSID"`
	Protected bool                  `json:"protected"`
	ACL       []clientProjectionACE `json:"acl"`
}
type clientProjectionImage struct {
	Path     string                 `json:"path"`
	SHA256   string                 `json:"sha256"`
	MaxBytes int64                  `json:"maxBytes,string"`
	Volume   clientProjectionVolume `json:"volume"`
	Nodes    []clientProjectionNode `json:"nodes"`
}

func projectClientVolume(v protectedkey.VolumeProfile) clientProjectionVolume {
	return clientProjectionVolume{Root: v.Root, Filesystem: v.Filesystem, Serial: v.Serial}
}
func projectClientACL(acl []protectedkey.ACE) []clientProjectionACE {
	out := make([]clientProjectionACE, len(acl))
	for i, ace := range acl {
		out[i] = clientProjectionACE{Type: ace.Type, Flags: ace.Flags, Mask: ace.Mask, SID: ace.SID}
	}
	return out
}
func projectClientSecurity(s serviceready.SecurityPolicy) clientProjectionSecurity {
	return clientProjectionSecurity{OwnerSID: s.OwnerSID, Protected: s.Protected, ACL: projectClientACL(s.ACL)}
}
func projectClientImage(image bootstrapauth.Image) clientProjectionImage {
	nodes := make([]clientProjectionNode, len(image.Nodes))
	for i, node := range image.Nodes {
		nodes[i] = clientProjectionNode{Path: node.Path, OwnerSID: node.OwnerSID, ACL: projectClientACL(node.ACL)}
	}
	return clientProjectionImage{Path: image.Path, SHA256: hex.EncodeToString(image.SHA256[:]), MaxBytes: image.MaxBytes, Volume: projectClientVolume(image.Volume), Nodes: nodes}
}

// DeriveUntrustedClientProjection checks source/build pairing only. The digest
// argument must come from the packaging caller's selected binary; a supplied
// digest or successful decode never proves filesystem or execution integrity.
func DeriveUntrustedClientProjection(recordBytes []byte, serviceBuildSHA256 [32]byte) (UntrustedClientProjection, error) {
	record, err := DecodeRecord(recordBytes)
	if err != nil || serviceBuildSHA256 == ([32]byte{}) || record.Profile.SupervisorImage.SHA256 != serviceBuildSHA256 {
		return UntrustedClientProjection{}, ErrRecord
	}
	// This initial client candidate is for the SYSTEM-hosted guest service only.
	// It does not relax the different control/1 client's TokenUser contract.
	if record.Profile.Keys.ProcessUserSID != "S-1-5-18" {
		return UntrustedClientProjection{}, ErrRecord
	}
	sum := sha256.Sum256(recordBytes)
	return UntrustedClientProjection{
		Protocol: "rescue-service-client-projection/1", Activation: "blocked", Trust: "unverified",
		ServiceCommandLine: record.ServiceCommandLine, ExcludedGuestSID: record.Profile.Keys.GuestSID,
		SourceProtocol: record.Protocol, RecordSHA256: hex.EncodeToString(sum[:]),
		ServiceBuildSHA256: hex.EncodeToString(serviceBuildSHA256[:]),
		ServiceName:        record.Profile.ServiceName, Endpoint: record.Endpoint,
		ProcessUserSID: record.Profile.Keys.ProcessUserSID, ServiceSID: record.Profile.Keys.ServiceSID,
		OperatorSID: record.Profile.Keys.OperatorSID, Volume: projectClientVolume(record.Profile.Keys.Volume),
		ServiceImage: projectClientImage(record.Profile.SupervisorImage), ServiceSecurity: projectClientSecurity(record.ServiceSecurity),
		ProcessSecurity: projectClientSecurity(record.ProcessSecurity),
	}, nil
}

// EncodeUntrustedClientProjection rederives the entire candidate instead of
// accepting caller-modifiable projection fields or policy/path overrides.
func EncodeUntrustedClientProjection(recordBytes []byte, serviceBuildSHA256 [32]byte) ([]byte, error) {
	projection, err := DeriveUntrustedClientProjection(recordBytes, serviceBuildSHA256)
	if err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(projection)
	if err != nil {
		return nil, ErrRecord
	}
	return append(encoded, '\n'), nil
}

// VerifyUntrustedClientProjection compares the exact canonical artifact to the
// same source/build pair. It validates no OS protection or native peer.
func VerifyUntrustedClientProjection(recordBytes []byte, serviceBuildSHA256 [32]byte, artifact []byte) error {
	expected, err := EncodeUntrustedClientProjection(recordBytes, serviceBuildSHA256)
	if err != nil || !bytes.Equal(expected, artifact) {
		return ErrRecord
	}
	return nil
}
