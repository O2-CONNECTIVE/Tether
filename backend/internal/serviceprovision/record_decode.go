package serviceprovision

import (
	"encoding/hex"
	"encoding/json"
	"strconv"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/bootstrap"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/bootstrapauth"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/operatorauth"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/protectedkey"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/serviceready"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/stdio"
	"github.com/O2-CONNECTIVE/rescue-host/backend/servicehost"
)

type wireACE struct {
	Type  string `json:"type"`
	Flags string `json:"flags"`
	Mask  string `json:"mask"`
	SID   string `json:"sid"`
}
type wireNode struct {
	Path     string    `json:"path"`
	OwnerSID string    `json:"ownerSid"`
	ACL      []wireACE `json:"acl"`
}
type wireVolume struct {
	Root       string `json:"root"`
	Filesystem string `json:"filesystem"`
	Serial     string `json:"serial"`
}
type wireProvisioning struct {
	Reference   string `json:"reference"`
	ProtectedAt string `json:"protectedAt"`
}
type wireKey struct {
	Path        string     `json:"path"`
	Fingerprint string     `json:"fingerprint"`
	Nodes       []wireNode `json:"nodes"`
}
type wireKeys struct {
	Version        string             `json:"version"`
	ProcessUserSID string             `json:"processUserSid"`
	ServiceSID     string             `json:"serviceSid"`
	OperatorSID    string             `json:"operatorSid"`
	GuestSID       string             `json:"guestSid"`
	Volume         wireVolume         `json:"volume"`
	Provisioning   wireProvisioning   `json:"provisioning"`
	Catalog        map[string]wireKey `json:"catalog"`
}
type wireImage struct {
	Path     string     `json:"path"`
	SHA256   string     `json:"sha256"`
	MaxBytes string     `json:"maxBytes"`
	Volume   wireVolume `json:"volume"`
	Nodes    []wireNode `json:"nodes"`
}
type wireProfile struct {
	Keys            wireKeys  `json:"keys"`
	ServiceName     string    `json:"serviceName"`
	SupervisorImage wireImage `json:"supervisorImage"`
	ChildImage      wireImage `json:"childImage"`
}
type wireSecurity struct {
	OwnerSID  string    `json:"ownerSid"`
	Protected bool      `json:"protected"`
	ACL       []wireACE `json:"acl"`
}
type wireBudgets struct {
	Startup string `json:"startupNs"`
	Cleanup string `json:"cleanupNs"`
	Observe string `json:"observeNs"`
}
type wireBackend struct {
	OperationMode   string   `json:"operationMode"`
	AllowedKeys     []string `json:"allowedKeys"`
	MaximumDuration string   `json:"maximumDurationNs"`
	LeaseDuration   string   `json:"leaseDurationNs"`
	MaxChannels     string   `json:"maxChannels"`
	MaxHandshakes   string   `json:"maxHandshakes"`
	MaxTransports   string   `json:"maxTransports"`
}
type wireLimits struct {
	FrameBytes string `json:"frameBytes"`
	Frames     string `json:"frames"`
	Cleanup    string `json:"cleanupTimeoutNs"`
}
type wireRecord struct {
	Protocol            string              `json:"protocol"`
	Profile             wireProfile         `json:"profile"`
	Endpoint            string              `json:"endpoint"`
	ServiceCommandLine  string              `json:"serviceCommandLine"`
	ServiceSecurity     wireSecurity        `json:"serviceSecurity"`
	ProcessSecurity     wireSecurity        `json:"processSecurity"`
	ReadyCleanupTimeout string              `json:"readyCleanupTimeoutNs"`
	Budgets             wireBudgets         `json:"budgets"`
	PolicyRef           string              `json:"policyRef"`
	PollInterval        string              `json:"pollIntervalNs"`
	OperationTimeout    string              `json:"operationTimeoutNs"`
	StdioCleanupTimeout string              `json:"stdioCleanupTimeoutNs"`
	StderrBytes         string              `json:"stderrBytes"`
	Backend             wireBackend         `json:"backend"`
	HostKeyRef          string              `json:"hostKeyRef"`
	Limits              wireLimits          `json:"limits"`
	HelperSupplier      *wireHelperSupplier `json:"helperSupplier,omitempty"`
}

type wireHelperSupplier struct {
	ServiceName        string       `json:"serviceName"`
	ServiceSID         string       `json:"serviceSid"`
	ServiceCommandLine string       `json:"serviceCommandLine"`
	Image              wireImage    `json:"image"`
	ServiceSecurity    wireSecurity `json:"serviceSecurity"`
	ProcessSecurity    wireSecurity `json:"processSecurity"`
	TokenSecurity      wireSecurity `json:"tokenSecurity"`
	ChildProcessSDDL   string       `json:"childProcessSddl"`
	ChildThreadSDDL    string       `json:"childThreadSddl"`
	Cleanup            string       `json:"cleanupNs"`
	RunBudget          string       `json:"runBudgetNs"`
}

// DecodeRecord は候補fileの純粋な値だけを返し、保護digestや実EOFを照合しない。
func DecodeRecord(data []byte) (Record, error) {
	if !strictRecord(data) {
		return Record{}, ErrRecord
	}
	var w wireRecord
	if json.Unmarshal(data, &w) != nil {
		return Record{}, ErrRecord
	}
	n := &recordNumbers{}
	p := w.Profile
	catalog := make(map[string]protectedkey.KeyProfile, len(p.Keys.Catalog))
	for ref, key := range p.Keys.Catalog {
		catalog[ref] = protectedkey.KeyProfile{Path: key.Path, Fingerprint: key.Fingerprint, Nodes: n.nodes(key.Nodes)}
	}
	at, err := time.Parse(time.RFC3339Nano, p.Keys.Provisioning.ProtectedAt)
	if err != nil || at.UTC().Format(time.RFC3339Nano) != p.Keys.Provisioning.ProtectedAt {
		return Record{}, ErrRecord
	}
	keys := protectedkey.Profile{Version: uint32(n.uint(p.Keys.Version, 32, true)), ProcessUserSID: p.Keys.ProcessUserSID, ServiceSID: p.Keys.ServiceSID, OperatorSID: p.Keys.OperatorSID, GuestSID: p.Keys.GuestSID, Volume: n.volume(p.Keys.Volume), Provisioning: protectedkey.ProvisioningRecord{Reference: p.Keys.Provisioning.Reference, ProtectedAt: at}, Catalog: catalog}
	r := Record{Protocol: w.Protocol, Profile: bootstrapauth.Profile{Keys: keys, ServiceName: p.ServiceName, SupervisorImage: n.image(p.SupervisorImage), ChildImage: n.image(p.ChildImage)}, Endpoint: w.Endpoint, ServiceCommandLine: w.ServiceCommandLine, ServiceSecurity: n.security(w.ServiceSecurity), ProcessSecurity: n.security(w.ProcessSecurity), ReadyCleanupTimeout: n.duration(w.ReadyCleanupTimeout), Budgets: servicehost.Budgets{Startup: n.duration(w.Budgets.Startup), Cleanup: n.duration(w.Budgets.Cleanup), Observe: n.duration(w.Budgets.Observe)}, PolicyRef: w.PolicyRef, PollInterval: n.duration(w.PollInterval), OperationTimeout: n.duration(w.OperationTimeout), StdioCleanupTimeout: n.duration(w.StdioCleanupTimeout), StderrBytes: n.uint(w.StderrBytes, 64, true), Backend: bootstrap.Config{OperationMode: w.Backend.OperationMode, AllowedKeys: w.Backend.AllowedKeys, MaximumDuration: n.duration(w.Backend.MaximumDuration), LeaseDuration: n.duration(w.Backend.LeaseDuration), MaxChannels: int(n.uint(w.Backend.MaxChannels, 6, true)), MaxHandshakes: int(n.uint(w.Backend.MaxHandshakes, 7, true)), MaxTransports: int(n.uint(w.Backend.MaxTransports, 7, true))}, HostKeyRef: w.HostKeyRef, Limits: stdio.Limits{FrameBytes: int(n.uint(w.Limits.FrameBytes, 31, true)), Frames: int(n.uint(w.Limits.Frames, 31, true)), CleanupTimeout: n.duration(w.Limits.Cleanup)}}
	if h := w.HelperSupplier; h != nil {
		r.HelperSupplier = &operatorauth.HelperSupplierConfig{ServiceName: h.ServiceName, ServiceSID: h.ServiceSID, ServiceCommandLine: h.ServiceCommandLine, Image: n.image(h.Image), ServiceSecurity: n.security(h.ServiceSecurity), ProcessSecurity: n.security(h.ProcessSecurity), TokenSecurity: n.security(h.TokenSecurity), ChildProcessSDDL: h.ChildProcessSDDL, ChildThreadSDDL: h.ChildThreadSDDL, Cleanup: n.duration(h.Cleanup), RunBudget: n.duration(h.RunBudget)}
	}
	if n.invalid || ValidateRecord(r) != nil {
		return Record{}, ErrRecord
	}
	return r, nil
}

type recordNumbers struct{ invalid bool }

func (n *recordNumbers) uint(s string, bits int, positive bool) uint64 {
	value, err := strconv.ParseUint(s, 10, bits)
	if err != nil || strconv.FormatUint(value, 10) != s || positive && value == 0 {
		n.invalid = true
		return 0
	}
	return value
}
func (n *recordNumbers) duration(s string) time.Duration { return time.Duration(n.uint(s, 63, true)) }
func (n *recordNumbers) volume(w wireVolume) protectedkey.VolumeProfile {
	return protectedkey.VolumeProfile{Root: w.Root, Filesystem: w.Filesystem, Serial: n.uint(w.Serial, 64, true)}
}
func (n *recordNumbers) acl(w []wireACE) []protectedkey.ACE {
	out := make([]protectedkey.ACE, len(w))
	for i, a := range w {
		out[i] = protectedkey.ACE{Type: uint8(n.uint(a.Type, 8, false)), Flags: uint8(n.uint(a.Flags, 8, false)), Mask: uint32(n.uint(a.Mask, 32, false)), SID: a.SID}
	}
	return out
}
func (n *recordNumbers) nodes(w []wireNode) []protectedkey.NodePolicy {
	out := make([]protectedkey.NodePolicy, len(w))
	for i, node := range w {
		out[i] = protectedkey.NodePolicy{Path: node.Path, OwnerSID: node.OwnerSID, ACL: n.acl(node.ACL)}
	}
	return out
}
func (n *recordNumbers) image(w wireImage) bootstrapauth.Image {
	hash, err := hex.DecodeString(w.SHA256)
	if err != nil || len(hash) != 32 || hex.EncodeToString(hash) != w.SHA256 {
		n.invalid = true
	}
	var digest [32]byte
	copy(digest[:], hash)
	return bootstrapauth.Image{Path: w.Path, SHA256: digest, MaxBytes: int64(n.uint(w.MaxBytes, 63, true)), Volume: n.volume(w.Volume), Nodes: n.nodes(w.Nodes)}
}
func (n *recordNumbers) security(w wireSecurity) serviceready.SecurityPolicy {
	return serviceready.SecurityPolicy{OwnerSID: w.OwnerSID, Protected: w.Protected, ACL: n.acl(w.ACL)}
}
