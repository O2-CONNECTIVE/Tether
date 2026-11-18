package main

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"mime"
	"os"
	"path/filepath"
	"strings"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentexec"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentwire"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/maclaunch"
	"golang.org/x/crypto/ssh"
)

type pairResult struct {
	RegistrationEndpoint string `json:"registration_endpoint"`
	HostLabel            string `json:"host_label"`
	PrincipalLabel       string `json:"principal_label"`
	PrincipalPublicKey   string `json:"principal_public_key"`
}

func parsePairResult(ct string, b []byte) (pairResult, error) {
	var x pairResult
	mediaType, _, mediaErr := mime.ParseMediaType(ct)
	members, fieldsErr := agentwire.Fields(b, "registration_endpoint", "host_label", "principal_label", "principal_public_key")
	if mediaErr != nil || mediaType != "application/json" || agentwire.CheckJSON(b, false) != nil || fieldsErr != nil || len(members) != 4 || json.Unmarshal(b, &x) != nil || x.RegistrationEndpoint == "" || x.HostLabel == "" || x.PrincipalLabel == "" {
		return x, errors.New("invalid")
	}
	parts := strings.Split(x.PrincipalPublicKey, " ")
	if len(parts) != 2 || parts[0] != "ssh-ed25519" {
		return x, errors.New("invalid")
	}
	key, e := base64.StdEncoding.DecodeString(parts[1])
	if e != nil {
		return x, e
	}
	pub, e := ssh.ParsePublicKey(key)
	if e != nil || pub.Type() != ssh.KeyAlgoED25519 {
		return x, errors.New("invalid")
	}
	return x, nil
}

var renameFile = os.Rename
var syncDir = func(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

func install(dir, policyPath, home string, priv ed25519.PrivateKey, signer ssh.Signer, result pairResult, oldPolicy []byte) (bool, error) {
	hash := sha256.Sum256(signer.PublicKey().Marshal())
	keyPath := filepath.Join(dir, "host-key-"+base64.RawURLEncoding.EncodeToString(hash[:]))
	pemBytes, e := ssh.MarshalPrivateKey(priv, "tether host key")
	if e != nil {
		return false, e
	}
	keyTemp, e := writeTemp(dir, ".pair-", pem.EncodeToMemory(pemBytes))
	if e != nil {
		return false, e
	}
	installDone := false
	defer func() {
		if !installDone {
			os.Remove(keyTemp)
			os.Remove(keyPath)
		}
	}()
	if e = renameFile(keyTemp, keyPath); e != nil {
		return false, e
	}
	policy := map[string]any{"protocol": "rescue-mac-resident-policy/1", "policy_digest": agentexec.MacCurrentUserPolicyDigest(), "registration_endpoint": result.RegistrationEndpoint, "host_label": result.HostLabel, "principal_label": result.PrincipalLabel, "principal_public_key": strings.SplitN(result.PrincipalPublicKey, " ", 2)[1], "host_key_path": keyPath, "host_key_fingerprint": ssh.FingerprintSHA256(signer.PublicKey()), "work_dir": home, "home": home, "cleanup_seconds": 10, "operations": []any{map[string]any{"id": maclaunch.ResidentFixedRequestID, "command": maclaunch.ResidentFixedCommand, "timeout_ms": 15000}}}
	raw, e := json.Marshal(policy)
	if e != nil {
		return false, e
	}
	if e = maclaunch.ValidateFixedResidentPolicy(raw, policyPath); e != nil {
		return false, e
	}
	pt, e := writeTemp(dir, ".pair-", raw)
	if e != nil {
		return false, e
	}
	defer os.Remove(pt)
	if e = renameFile(pt, policyPath); e != nil {
		return false, e
	}
	// The rename is the commit: the gateway already holds the new key, so a
	// failed directory sync must not undo it (the old key no longer pairs).
	syncWarning := syncDir(dir) != nil
	if len(oldPolicy) > 0 {
		var previous struct {
			KeyPath string `json:"host_key_path"`
		}
		if json.Unmarshal(oldPolicy, &previous) == nil && filepath.Dir(previous.KeyPath) == dir && strings.HasPrefix(filepath.Base(previous.KeyPath), "host-key-") && previous.KeyPath != keyPath {
			_ = os.Remove(previous.KeyPath)
		}
	}
	installDone = true
	return syncWarning, nil
}
func writeTemp(dir, prefix string, b []byte) (string, error) {
	f, e := os.CreateTemp(dir, prefix)
	if e != nil {
		return "", e
	}
	name := f.Name()
	ok := false
	defer func() {
		f.Close()
		if !ok {
			os.Remove(name)
		}
	}()
	if e = f.Chmod(0600); e != nil {
		return "", e
	}
	if _, e = f.Write(b); e != nil {
		return "", e
	}
	if e = f.Sync(); e != nil {
		return "", e
	}
	if e = f.Close(); e != nil {
		return "", e
	}
	ok = true
	return name, nil
}

var _ io.Writer
