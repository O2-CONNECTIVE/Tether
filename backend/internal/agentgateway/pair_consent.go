package agentgateway

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"html/template"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/agentwire"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/hostpairoauth"
	"golang.org/x/crypto/ssh"
)

const (
	PairOperationsPath      = PairPath + "/operations"
	PairConsentPath         = PairPath + "/consent"
	PairConsentCallbackPath = PairConsentPath + "/callback"
	PairConfirmPath         = PairPath + "/confirm"
	pairConsentCookie       = "__Host-tether-pair-consent"
	maxPairOperations       = 64
	maxPairConsentStates    = 64
	maxPairConsentForms     = 64
)

var ErrRegistryDurabilityUnknown = errors.New("agentgateway: registry durability unknown")

// PairConsentConfig is the configured public OAuth client used for host pairing consent.
type PairConsentConfig struct {
	ClientID, AuthorizationEndpoint, TokenEndpoint string
	HTTPClient                                     *http.Client
}

type pairOperation struct {
	id, issuer, subject, hostLabel, principalLabel string
	revision                                       string
	newKey                                         []byte
	replacing                                      *PairRecord
	created, expires                               time.Time
	state                                          string
	terminalStatus                                 int
	terminalError                                  string
}

type pairConsentState struct {
	operation, verifier string
	cookieHash          [32]byte
	expires             time.Time
}

type pairConsentForm struct {
	operation  string
	cookieHash [32]byte
	expires    time.Time
}

type pairCommitResult struct {
	errorCode string
	unknown   bool
}

func PairOperationTranscript(endpoint string, hostKey []byte, tokenHash [32]byte) []byte {
	return pairConsentTranscript([]byte("rescue-host.agentgateway.pair-operation\x00v1\x00"), []byte(endpoint), hostKey, tokenHash[:])
}

func PairConfirmTranscript(endpoint, operation string, hostKey []byte) []byte {
	return pairConsentTranscript([]byte("rescue-host.agentgateway.pair-confirm\x00v1\x00"), []byte(endpoint), []byte(operation), hostKey)
}

func pairConsentTranscript(parts ...[]byte) []byte {
	var out bytes.Buffer
	for _, part := range parts {
		_ = binary.Write(&out, binary.BigEndian, uint32(len(part)))
		_, _ = out.Write(part)
	}
	return out.Bytes()
}

func (g *Gateway) ServePairOperations(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "", http.StatusMethodNotAllowed)
		return
	}
	body, ok := readPairBody(w, r)
	if !ok {
		return
	}
	g.mu.Lock()
	cfg := g.pairing
	g.mu.Unlock()
	if cfg == nil || cfg.Consent == nil {
		http.NotFound(w, r)
		return
	}
	identity, token, valid := verifyPairingBearer(w, r, cfg)
	if !valid {
		return
	}
	if agentwire.CheckJSON(body, false) != nil {
		http.Error(w, "", http.StatusBadRequest)
		return
	}
	key, proof, err := parsePairRequest(body)
	if err != nil || !verifyPairProof(key, proof, PairOperationTranscript(publicPairURL(cfg, PairOperationsPath), key.Marshal(), sha256.Sum256([]byte(token)))) {
		http.Error(w, "", http.StatusBadRequest)
		return
	}

	g.pairMu.Lock()
	defer g.pairMu.Unlock()
	now := g.clock().UTC()
	g.prunePairOperationsLocked(now)
	g.mu.Lock()
	principal, label, target, ambiguous := pairedSlotForIdentityLocked(g, identity)
	if target == nil || ambiguous {
		g.mu.Unlock()
		http.Error(w, "", http.StatusForbidden)
		return
	}
	if target.replacing || principal.route != nil || principal.connecting || residentCurrentLocked(g, principal) {
		g.mu.Unlock()
		writePairError(w, http.StatusConflict, "replace_blocked")
		return
	}
	if g.hostKeyInUseLocked(key.Marshal(), target) {
		g.mu.Unlock()
		writePairError(w, http.StatusConflict, "key_in_use")
		return
	}
	replacing := clonePairRecordPointer(target.record)
	revision := pairRecordRevision(replacing)
	principalLabel := principal.label
	g.mu.Unlock()

	// A newer preview for this slot revokes every unfinished earlier decision.
	for id, old := range g.pairOperations {
		if old.hostLabel == label && old.state != "consumed" {
			g.removePairOperationLocked(id)
		}
	}
	if len(g.pairOperations) >= maxPairOperations {
		writePairError(w, http.StatusServiceUnavailable, "operation_limit")
		return
	}
	id, err := randomPairValue()
	if err != nil {
		writePairError(w, http.StatusServiceUnavailable, "operation_unavailable")
		return
	}
	operation := &pairOperation{
		id: id, issuer: identity.Issuer, subject: identity.Subject, hostLabel: label,
		principalLabel: principalLabel, revision: revision, newKey: append([]byte(nil), key.Marshal()...),
		replacing: replacing, created: now, expires: now.Add(10 * time.Minute), state: "pending",
	}
	g.pairOperations[id] = operation
	consentURL, _ := url.Parse(publicPairURL(cfg, PairConsentPath))
	q := consentURL.Query()
	q.Set("operation", id)
	consentURL.RawQuery = q.Encode()
	response := map[string]any{
		"operation":           id,
		"account":             map[string]string{"issuer": identity.Issuer, "subject": identity.Subject, "principal_label": principalLabel},
		"host_label":          label,
		"replacing":           pairReplacementView(replacing),
		"new_key_fingerprint": fingerprintPairKey(key),
		"consent_url":         consentURL.String(),
		"expires_at":          operation.expires.Format(time.RFC3339),
	}
	writePairJSON(w, http.StatusOK, response)
}

func (g *Gateway) ServePairConsent(w http.ResponseWriter, r *http.Request) {
	setConsentPageHeaders(w)
	if r.Method == http.MethodGet {
		g.startPairConsent(w, r)
		return
	}
	if r.Method == http.MethodPost {
		g.decidePairConsent(w, r)
		return
	}
	w.Header().Set("Allow", "GET, POST")
	writePairConsentResult(w, http.StatusMethodNotAllowed, pairConsentRejectedHTML)
}

func (g *Gateway) startPairConsent(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	cfg := g.pairing
	g.mu.Unlock()
	if cfg == nil || cfg.Consent == nil {
		http.NotFound(w, r)
		return
	}
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil || len(query) != 1 || len(query["operation"]) != 1 || !validPairOperationID(query.Get("operation")) {
		http.Error(w, "", http.StatusBadRequest)
		return
	}
	id := query.Get("operation")
	g.pairMu.Lock()
	now := g.clock().UTC()
	g.prunePairOperationsLocked(now)
	operation := g.pairOperations[id]
	if operation == nil || operation.state != "pending" || !now.Before(operation.expires) {
		g.pairMu.Unlock()
		http.NotFound(w, r)
		return
	}
	g.removePairStatesLocked(id)
	if len(g.pairConsentStates) >= maxPairConsentStates {
		g.pairMu.Unlock()
		writePairError(w, http.StatusServiceUnavailable, "consent_unavailable")
		return
	}
	state, err := randomPairValue()
	if err != nil {
		g.pairMu.Unlock()
		writePairError(w, http.StatusServiceUnavailable, "consent_unavailable")
		return
	}
	verifier, err := randomPairValue()
	if err != nil {
		g.pairMu.Unlock()
		writePairError(w, http.StatusServiceUnavailable, "consent_unavailable")
		return
	}
	cookieValue, cookieHash, validCookie := consentCookie(r)
	if !validCookie {
		cookieValue, err = randomPairValue()
		if err != nil {
			g.pairMu.Unlock()
			writePairError(w, http.StatusServiceUnavailable, "consent_unavailable")
			return
		}
		cookieHash = sha256.Sum256([]byte(cookieValue))
	}
	if g.pairConsentStates == nil {
		g.pairConsentStates = make(map[string]*pairConsentState)
	}
	g.pairConsentStates[state] = &pairConsentState{operation: id, verifier: verifier, cookieHash: cookieHash, expires: operation.expires}
	operationExpiry := operation.expires
	g.pairMu.Unlock()

	http.SetCookie(w, &http.Cookie{Name: pairConsentCookie, Value: cookieValue, Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode, Expires: operationExpiry, MaxAge: int(operationExpiry.Sub(now).Seconds())})
	authorization, err := url.Parse(cfg.Consent.AuthorizationEndpoint)
	if err != nil {
		writePairError(w, http.StatusInternalServerError, "consent_unavailable")
		return
	}
	challenge := sha256.Sum256([]byte(verifier))
	q := authorization.Query()
	q.Set("response_type", "code")
	q.Set("client_id", cfg.Consent.ClientID)
	q.Set("redirect_uri", publicPairURL(cfg, PairConsentCallbackPath))
	q.Set("code_challenge", base64.RawURLEncoding.EncodeToString(challenge[:]))
	q.Set("code_challenge_method", "S256")
	q.Set("state", state)
	authorization.RawQuery = q.Encode()
	http.Redirect(w, r, authorization.String(), http.StatusFound)
}

func (g *Gateway) ServePairConsentCallback(w http.ResponseWriter, r *http.Request) {
	setConsentPageHeaders(w)
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "", http.StatusMethodNotAllowed)
		return
	}
	g.mu.Lock()
	cfg := g.pairing
	g.mu.Unlock()
	if cfg == nil || cfg.Consent == nil {
		http.NotFound(w, r)
		return
	}
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil || len(query["state"]) != 1 || len(query["code"]) != 1 || query.Get("state") == "" || query.Get("code") == "" || query.Has("error") {
		http.Error(w, "", http.StatusBadRequest)
		return
	}
	cookieHash, ok := consentCookieHash(r)
	if !ok {
		writePairError(w, http.StatusForbidden, "consent_invalid")
		return
	}
	state := query.Get("state")
	g.pairMu.Lock()
	now := g.clock().UTC()
	g.prunePairOperationsLocked(now)
	transaction := g.pairConsentStates[state]
	if transaction == nil || !now.Before(transaction.expires) || subtle.ConstantTimeCompare(cookieHash[:], transaction.cookieHash[:]) != 1 {
		g.pairMu.Unlock()
		writePairError(w, http.StatusForbidden, "consent_invalid")
		return
	}
	delete(g.pairConsentStates, state)
	operation := g.pairOperations[transaction.operation]
	if operation == nil || operation.state != "pending" || !now.Before(operation.expires) {
		g.pairMu.Unlock()
		http.NotFound(w, r)
		return
	}
	verifier, operationID := transaction.verifier, operation.id
	g.pairMu.Unlock()

	client := pairConsentHTTPClient(cfg.Consent.HTTPClient)
	token, err := hostpairoauth.ExchangeAuthorizationCode(r.Context(), client, cfg.Consent.TokenEndpoint, query.Get("code"), publicPairURL(cfg, PairConsentCallbackPath), cfg.Consent.ClientID, verifier)
	if err != nil {
		writePairError(w, http.StatusBadGateway, "consent_exchange_failed")
		return
	}
	identity, err := cfg.Verifier.Verify(token)
	if err != nil {
		writePairError(w, http.StatusForbidden, "consent_invalid")
		return
	}

	g.pairMu.Lock()
	now = g.clock().UTC()
	operation = g.pairOperations[operationID]
	if operation == nil || operation.state != "pending" || !now.Before(operation.expires) {
		g.pairMu.Unlock()
		http.NotFound(w, r)
		return
	}
	if identity.Issuer != operation.issuer || identity.Subject != operation.subject {
		g.pairMu.Unlock()
		writePairError(w, http.StatusForbidden, "account_mismatch")
		return
	}
	formToken, err := randomPairValue()
	if err != nil {
		g.pairMu.Unlock()
		writePairError(w, http.StatusServiceUnavailable, "consent_unavailable")
		return
	}
	g.removePairFormsLocked(operationID)
	if len(g.pairConsentForms) >= maxPairConsentForms {
		g.pairMu.Unlock()
		writePairError(w, http.StatusServiceUnavailable, "consent_unavailable")
		return
	}
	if g.pairConsentForms == nil {
		g.pairConsentForms = make(map[string]*pairConsentForm)
	}
	g.pairConsentForms[formToken] = &pairConsentForm{operation: operationID, cookieHash: cookieHash, expires: operation.expires}
	g.mu.Lock()
	principal := g.bySubject[operation.subject]
	slot := g.hosts[operation.hostLabel]
	if principal == nil || slot == nil || slot.paired != principal {
		g.mu.Unlock()
		g.pairMu.Unlock()
		http.NotFound(w, r)
		return
	}
	view := pairConsentPageView{
		Operation: operationID, FormToken: formToken, PrincipalLabel: operation.principalLabel,
		Subject: operation.subject, HostLabel: operation.hostLabel,
		NewFingerprint: fingerprintPairBytes(operation.newKey),
	}
	if operation.replacing != nil {
		view.Replacing = true
		view.ReplacingFingerprint = fingerprintPairBytes(operation.replacing.HostKey)
		view.RegisteredAt = operation.replacing.RegisteredAt.UTC().Format(time.RFC3339)
	} else {
		view.ReplacingLabel = "新規"
	}
	g.mu.Unlock()
	g.pairMu.Unlock()
	writePairConsentPage(w, view)
}

func (g *Gateway) decidePairConsent(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	cfg := g.pairing
	g.mu.Unlock()
	if cfg == nil || cfg.Consent == nil {
		writePairConsentResult(w, http.StatusNotFound, pairConsentRejectedHTML)
		return
	}
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/x-www-form-urlencoded" {
		writePairConsentResult(w, http.StatusBadRequest, pairConsentRejectedHTML)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, MaxPairBody+1))
	if err != nil {
		writePairConsentResult(w, http.StatusBadRequest, pairConsentRejectedHTML)
		return
	}
	if len(body) > MaxPairBody {
		writePairConsentResult(w, http.StatusRequestEntityTooLarge, pairConsentRejectedHTML)
		return
	}
	form, err := url.ParseQuery(string(body))
	if err != nil || len(form) != 3 || len(form["operation"]) != 1 || len(form["form_token"]) != 1 || len(form["decision"]) != 1 {
		writePairConsentResult(w, http.StatusBadRequest, pairConsentRejectedHTML)
		return
	}
	id, token, decision := form.Get("operation"), form.Get("form_token"), form.Get("decision")
	if !validPairOperationID(id) || !validPairOperationID(token) || (decision != "confirm" && decision != "cancel") {
		writePairConsentResult(w, http.StatusBadRequest, pairConsentRejectedHTML)
		return
	}
	cookieHash, ok := consentCookieHash(r)
	if !ok {
		writePairConsentResult(w, http.StatusForbidden, pairConsentRejectedHTML)
		return
	}
	g.pairMu.Lock()
	defer g.pairMu.Unlock()
	now := g.clock().UTC()
	g.prunePairOperationsLocked(now)
	operation := g.pairOperations[id]
	formRecord := g.pairConsentForms[token]
	if operation == nil || formRecord == nil || formRecord.operation != id || !now.Before(formRecord.expires) || subtle.ConstantTimeCompare(cookieHash[:], formRecord.cookieHash[:]) != 1 {
		writePairConsentResult(w, http.StatusForbidden, pairConsentRejectedHTML)
		return
	}
	if operation.state != "pending" || !now.Before(operation.expires) {
		if operation.terminalStatus != 0 {
			writePairConsentResult(w, operation.terminalStatus, pairConsentRejectedHTML)
		} else {
			writePairConsentResult(w, http.StatusConflict, pairConsentRejectedHTML)
		}
		return
	}
	if decision == "cancel" {
		operation.state = "cancelled"
		operation.terminalStatus = http.StatusForbidden
		operation.terminalError = "consent_cancelled"
		g.removePairStatesLocked(id)
		g.removePairFormsLocked(id)
		writePairConsentResult(w, http.StatusOK, pairConsentCancelledHTML)
		return
	}
	g.mu.Lock()
	target := g.hosts[operation.hostLabel]
	changed := target == nil || pairRecordRevision(target.record) != operation.revision
	g.mu.Unlock()
	if changed {
		operation.state = "terminal"
		operation.terminalStatus = http.StatusConflict
		operation.terminalError = "revision_changed"
		g.removePairStatesLocked(id)
		g.removePairFormsLocked(id)
		writePairConsentResult(w, http.StatusConflict, pairConsentRejectedHTML)
		return
	}
	operation.state = "consented"
	g.removePairStatesLocked(id)
	g.removePairFormsLocked(id)
	writePairConsentResult(w, http.StatusOK, pairConsentConfirmedHTML)
}

func (g *Gateway) ServePairConfirm(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "", http.StatusMethodNotAllowed)
		return
	}
	body, ok := readPairBody(w, r)
	if !ok {
		return
	}
	g.mu.Lock()
	cfg := g.pairing
	g.mu.Unlock()
	if cfg == nil || cfg.Consent == nil {
		writePairError(w, http.StatusNotFound, "operation_not_found")
		return
	}
	if agentwire.CheckJSON(body, false) != nil {
		http.Error(w, "", http.StatusBadRequest)
		return
	}
	members, err := agentwire.Fields(body, "operation", "proof")
	var id, proofText string
	if err != nil || len(members) != 2 || json.Unmarshal(members["operation"], &id) != nil || json.Unmarshal(members["proof"], &proofText) != nil || !validPairOperationID(id) || proofText == "" {
		http.Error(w, "", http.StatusBadRequest)
		return
	}

	g.pairMu.Lock()
	defer g.pairMu.Unlock()
	now := g.clock().UTC()
	g.mu.Lock()
	stored := clonePairRecords(cfg.Records)
	g.mu.Unlock()
	for _, record := range stored {
		if record.Operation != id {
			continue
		}
		if !verifyPairProofBytes(record.HostKey, proofText, PairConfirmTranscript(publicPairURL(cfg, PairConfirmPath), id, record.HostKey)) {
			http.Error(w, "", http.StatusBadRequest)
			return
		}
		if g.uncertainOperations[id] {
			writePairError(w, http.StatusInternalServerError, "registration_unknown")
			return
		}
		response := g.pairSuccessResponse(record.HostLabel, record.Subject)
		if response == nil {
			writePairError(w, http.StatusInternalServerError, "registration_unknown")
			return
		}
		writePairJSON(w, http.StatusOK, response)
		return
	}
	g.prunePairOperationsLocked(now)
	operation := g.pairOperations[id]
	if operation == nil || (!now.Before(operation.expires) && operation.terminalStatus == 0) {
		writePairError(w, http.StatusNotFound, "operation_not_found")
		return
	}
	if !verifyPairProofBytes(operation.newKey, proofText, PairConfirmTranscript(publicPairURL(cfg, PairConfirmPath), id, operation.newKey)) {
		http.Error(w, "", http.StatusBadRequest)
		return
	}
	if operation.terminalStatus != 0 {
		writePairError(w, operation.terminalStatus, operation.terminalError)
		return
	}
	switch operation.state {
	case "pending":
		writePairJSON(w, http.StatusAccepted, map[string]string{"state": "consent_pending"})
		return
	case "cancelled":
		writePairError(w, http.StatusForbidden, "consent_cancelled")
		return
	case "consented":
		// Continue into the shared reservation, save, reflection, and disconnect path below.
	default:
		writePairError(w, http.StatusNotFound, "operation_not_found")
		return
	}
	newRecord := PairRecord{HostLabel: operation.hostLabel, HostKey: append([]byte(nil), operation.newKey...), Issuer: operation.issuer, Subject: operation.subject, RegisteredAt: now.Truncate(time.Second), Operation: operation.id}
	result := g.commitPairRecordLocked(cfg, operation.hostLabel, operation.subject, newRecord, operation.revision, true)
	switch result.errorCode {
	case "replace_blocked", "key_in_use", "revision_changed":
		operation.state = "terminal"
		operation.terminalStatus = http.StatusConflict
		operation.terminalError = result.errorCode
		g.removePairFormsLocked(id)
		writePairError(w, http.StatusConflict, result.errorCode)
	case "save_failed":
		operation.state = "terminal"
		operation.terminalStatus = http.StatusServiceUnavailable
		operation.terminalError = "save_failed"
		g.removePairFormsLocked(id)
		writePairError(w, http.StatusServiceUnavailable, "save_failed")
	case "registration_unknown":
		operation.state = "terminal"
		operation.terminalStatus = http.StatusInternalServerError
		operation.terminalError = "registration_unknown"
		g.removePairFormsLocked(id)
		writePairError(w, http.StatusInternalServerError, "registration_unknown")
	default:
		operation.state = "consumed"
		response := g.pairSuccessResponse(operation.hostLabel, operation.subject)
		if response == nil {
			operation.state = "terminal"
			operation.terminalStatus = http.StatusInternalServerError
			operation.terminalError = "registration_unknown"
			writePairError(w, http.StatusInternalServerError, "registration_unknown")
			return
		}
		writePairJSON(w, http.StatusOK, response)
	}
}

func (g *Gateway) commitPairRecordLocked(cfg *PairingConfig, label, subject string, record PairRecord, expectedRevision string, checkRevision bool) pairCommitResult {
	g.mu.Lock()
	target := g.hosts[label]
	principal := g.bySubject[subject]
	if target == nil || principal == nil || target.paired != principal {
		g.mu.Unlock()
		return pairCommitResult{errorCode: "registration_unknown"}
	}
	if target.replacing || principal.route != nil || principal.connecting || residentCurrentLocked(g, principal) {
		g.mu.Unlock()
		return pairCommitResult{errorCode: "replace_blocked"}
	}
	if g.hostKeyInUseLocked(record.HostKey, target) {
		g.mu.Unlock()
		return pairCommitResult{errorCode: "key_in_use"}
	}
	if checkRevision && pairRecordRevision(target.record) != expectedRevision {
		g.mu.Unlock()
		return pairCommitResult{errorCode: "revision_changed"}
	}
	target.replacing = true
	recordCopy := record
	recordCopy.HostKey = append([]byte(nil), record.HostKey...)
	records := clonePairRecords(cfg.Records)
	found := false
	for i := range records {
		if records[i].HostLabel == label {
			records[i] = recordCopy
			found = true
			break
		}
	}
	if !found {
		records = append(records, recordCopy)
	}
	g.mu.Unlock()

	var saveErr error
	if cfg.Save != nil {
		saveErr = cfg.Save(clonePairRecords(records))
	}
	unknown := errors.Is(saveErr, ErrRegistryDurabilityUnknown)
	if saveErr != nil && !unknown {
		g.mu.Lock()
		target.replacing = false
		g.mu.Unlock()
		return pairCommitResult{errorCode: "save_failed"}
	}
	g.mu.Lock()
	target.key = append([]byte(nil), recordCopy.HostKey...)
	target.record = &recordCopy
	cfg.Records = clonePairRecords(records)
	if unknown && record.Operation != "" {
		if g.uncertainOperations == nil {
			g.uncertainOperations = make(map[string]bool)
		}
		g.uncertainOperations[record.Operation] = true
	}
	g.mu.Unlock()
	if cfg.Disconnect != nil {
		cfg.Disconnect(label)
	}
	g.mu.Lock()
	target.replacing = false
	g.mu.Unlock()
	if unknown {
		return pairCommitResult{errorCode: "registration_unknown", unknown: true}
	}
	return pairCommitResult{}
}

func (g *Gateway) pairSuccessResponse(hostLabel, subject string) map[string]string {
	g.mu.Lock()
	defer g.mu.Unlock()
	p := g.bySubject[subject]
	if p == nil || p.signer == nil || g.hosts[hostLabel] == nil {
		return nil
	}
	principalKey := strings.TrimSuffix(string(ssh.MarshalAuthorizedKey(p.signer.PublicKey())), "\n")
	return map[string]string{"registration_endpoint": g.pairing.RegistrationEndpoint, "host_label": hostLabel, "principal_label": p.label, "principal_public_key": principalKey}
}

func verifyPairingBearer(w http.ResponseWriter, r *http.Request, cfg *PairingConfig) (Identity, string, bool) {
	headers := r.Header.Values("Authorization")
	token := ""
	if len(headers) == 1 {
		fields := strings.Fields(headers[0])
		if len(fields) == 2 && strings.EqualFold(fields[0], "Bearer") {
			token = fields[1]
		}
	}
	identity, err := cfg.Verifier.Verify(token)
	if err != nil {
		challenge := "Bearer error=\"invalid_token\""
		if len(headers) == 0 {
			challenge = "Bearer"
		}
		w.Header().Set("WWW-Authenticate", challenge)
		http.Error(w, "", http.StatusUnauthorized)
		return Identity{}, "", false
	}
	return identity, token, true
}

func pairedSlotForIdentityLocked(g *Gateway, identity Identity) (*principal, string, *hostSlot, bool) {
	if identity.Issuer != g.verifier.issuer {
		return nil, "", nil, false
	}
	p := g.bySubject[identity.Subject]
	if p == nil {
		return nil, "", nil, false
	}
	var label string
	var target *hostSlot
	for name, slot := range g.hosts {
		if slot.paired == p {
			if target != nil {
				return p, "", nil, true
			}
			label, target = name, slot
		}
	}
	return p, label, target, false
}

func residentCurrentLocked(g *Gateway, p *principal) bool {
	entry := g.residentEntryForPrincipalLocked(p)
	return entry != nil && entry.current != nil
}

func parsePairRequest(body []byte) (ssh.PublicKey, string, error) {
	members, err := agentwire.Fields(body, "host_public_key", "proof")
	if err != nil || len(members) != 2 {
		return nil, "", errors.New("pair request fields invalid")
	}
	var keyText, proofText string
	if json.Unmarshal(members["host_public_key"], &keyText) != nil || json.Unmarshal(members["proof"], &proofText) != nil || keyText == "" || proofText == "" {
		return nil, "", errors.New("pair request values invalid")
	}
	key, comment, options, rest, err := ssh.ParseAuthorizedKey([]byte(keyText))
	if err != nil || comment != "" || len(options) != 0 || len(rest) != 0 || key.Type() != ssh.KeyAlgoED25519 || smallOrderPairKey(key) || strings.TrimSuffix(string(ssh.MarshalAuthorizedKey(key)), "\n") != keyText {
		return nil, "", errors.New("pair host key invalid")
	}
	return key, proofText, nil
}

func verifyPairProof(key ssh.PublicKey, encoded string, transcript []byte) bool {
	return verifyPairProofBytes(key.Marshal(), encoded, transcript)
}

func verifyPairProofBytes(rawKey []byte, encoded string, transcript []byte) bool {
	key, err := ssh.ParsePublicKey(rawKey)
	if err != nil || key.Type() != ssh.KeyAlgoED25519 || smallOrderPairKey(key) {
		return false
	}
	sigWire, err := base64.RawURLEncoding.Strict().DecodeString(encoded)
	if err != nil {
		return false
	}
	var signature struct {
		Format string
		Blob   []byte
		Rest   []byte `ssh:"rest"`
	}
	if ssh.Unmarshal(sigWire, &signature) != nil || len(signature.Rest) != 0 || signature.Format != ssh.KeyAlgoED25519 || len(signature.Blob) != 64 {
		return false
	}
	return key.Verify(transcript, &ssh.Signature{Format: signature.Format, Blob: signature.Blob}) == nil
}

func smallOrderPairKey(key ssh.PublicKey) bool {
	if key == nil || key.Type() != ssh.KeyAlgoED25519 {
		return true
	}
	raw := key.Marshal()
	if len(raw) < 32 {
		return true
	}
	return smallOrderEd25519Encoding(raw[len(raw)-32:])
}

func smallOrderEd25519Encoding(encoded []byte) bool {
	if len(encoded) != 32 {
		return false
	}
	for _, point := range pairSmallOrderEd25519Encodings {
		var diff byte
		for i := 0; i < len(encoded)-1; i++ {
			diff |= encoded[i] ^ point[i]
		}
		diff |= (encoded[31] & 0x7f) ^ (point[31] & 0x7f)
		if diff == 0 {
			return true
		}
	}
	return false
}

var pairSmallOrderEd25519Encodings = [12][32]byte{
	{0x00},
	{0x01},
	{0x26, 0xe8, 0x95, 0x8f, 0xc2, 0xb2, 0x27, 0xb0, 0x45, 0xc3, 0xf4, 0x89, 0xf2, 0xef, 0x98, 0xf0, 0xd5, 0xdf, 0xac, 0x05, 0xd3, 0xc6, 0x33, 0x39, 0xb1, 0x38, 0x02, 0x88, 0x6d, 0x53, 0xfc, 0x05},
	{0xc7, 0x17, 0x6a, 0x70, 0x3d, 0x4d, 0xd8, 0x4f, 0xba, 0x3c, 0x0b, 0x76, 0x0d, 0x10, 0x67, 0x0f, 0x2a, 0x20, 0x53, 0xfa, 0x2c, 0x39, 0xcc, 0xc6, 0x4e, 0xc7, 0xfd, 0x77, 0x92, 0xac, 0x03, 0x7a},
	{0x13, 0xe8, 0x95, 0x8f, 0xc2, 0xb2, 0x27, 0xb0, 0x45, 0xc3, 0xf4, 0x89, 0xf2, 0xef, 0x98, 0xf0, 0xd5, 0xdf, 0xac, 0x05, 0xd3, 0xc6, 0x33, 0x39, 0xb1, 0x38, 0x02, 0x88, 0x6d, 0x53, 0xfc, 0x85},
	{0xb4, 0x17, 0x6a, 0x70, 0x3d, 0x4d, 0xd8, 0x4f, 0xba, 0x3c, 0x0b, 0x76, 0x0d, 0x10, 0x67, 0x0f, 0x2a, 0x20, 0x53, 0xfa, 0x2c, 0x39, 0xcc, 0xc6, 0x4e, 0xc7, 0xfd, 0x77, 0x92, 0xac, 0x03, 0xfa},
	{0xec, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0x7f},
	{0xed, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0x7f},
	{0xee, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0x7f},
	{0xd9, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff},
	{0xda, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff},
	{0xdb, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff},
}

func readPairBody(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	body, err := io.ReadAll(io.LimitReader(r.Body, MaxPairBody+1))
	if err != nil {
		http.Error(w, "", http.StatusBadRequest)
		return nil, false
	}
	if len(body) > MaxPairBody {
		http.Error(w, "", http.StatusRequestEntityTooLarge)
		return nil, false
	}
	return body, true
}

func pairConsentHTTPClient(client *http.Client) *http.Client {
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	copy := *client
	copy.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &copy
}

func publicPairURL(cfg *PairingConfig, path string) string {
	u, _ := url.Parse(cfg.Endpoint)
	u.Path = path
	u.RawPath = ""
	u.RawQuery = ""
	u.Fragment = ""
	return u.String()
}

func randomPairValue() (string, error) {
	value := make([]byte, 32)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func validPairOperationID(value string) bool {
	decoded, err := base64.RawURLEncoding.Strict().DecodeString(value)
	return err == nil && len(decoded) == 32 && base64.RawURLEncoding.EncodeToString(decoded) == value
}

// ValidPairOperationID checks the canonical 32-byte operation encoding persisted in the registry.
func ValidPairOperationID(value string) bool { return validPairOperationID(value) }

func consentCookie(r *http.Request) (string, [32]byte, bool) {
	var digest [32]byte
	var value string
	count := 0
	for _, cookie := range r.Cookies() {
		if cookie.Name == pairConsentCookie {
			count++
			value = cookie.Value
		}
	}
	if count != 1 || !validPairOperationID(value) {
		return "", digest, false
	}
	return value, sha256.Sum256([]byte(value)), true
}

func consentCookieHash(r *http.Request) ([32]byte, bool) {
	_, digest, ok := consentCookie(r)
	return digest, ok
}

func (g *Gateway) prunePairOperationsLocked(now time.Time) {
	for id, operation := range g.pairOperations {
		if !now.Before(operation.expires) && operation.terminalStatus == 0 {
			g.removePairOperationLocked(id)
		}
	}
	for state, transaction := range g.pairConsentStates {
		if !now.Before(transaction.expires) || g.pairOperations[transaction.operation] == nil {
			delete(g.pairConsentStates, state)
		}
	}
	for token, form := range g.pairConsentForms {
		if !now.Before(form.expires) || g.pairOperations[form.operation] == nil {
			delete(g.pairConsentForms, token)
		}
	}
}

func (g *Gateway) removePairOperationLocked(id string) {
	delete(g.pairOperations, id)
	g.removePairStatesLocked(id)
	g.removePairFormsLocked(id)
}

func (g *Gateway) removePairStatesLocked(id string) {
	for state, transaction := range g.pairConsentStates {
		if transaction.operation == id {
			delete(g.pairConsentStates, state)
		}
	}
}

func (g *Gateway) removePairFormsLocked(id string) {
	for token, form := range g.pairConsentForms {
		if form.operation == id {
			delete(g.pairConsentForms, token)
		}
	}
}

func clonePairRecordPointer(record *PairRecord) *PairRecord {
	if record == nil {
		return nil
	}
	copy := *record
	copy.HostKey = append([]byte(nil), record.HostKey...)
	return &copy
}

func pairRecordRevision(record *PairRecord) string {
	if record == nil {
		return ""
	}
	key, err := ssh.ParsePublicKey(record.HostKey)
	if err != nil {
		return "invalid"
	}
	canonical := struct {
		Label        string `json:"label"`
		PublicKey    string `json:"public_key"`
		Fingerprint  string `json:"fingerprint"`
		Issuer       string `json:"issuer"`
		Subject      string `json:"subject"`
		RegisteredAt string `json:"registered_at"`
		Operation    string `json:"operation,omitempty"`
	}{
		Label:        record.HostLabel,
		PublicKey:    strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key))),
		Fingerprint:  ssh.FingerprintSHA256(key),
		Issuer:       record.Issuer,
		Subject:      record.Subject,
		RegisteredAt: record.RegisteredAt.UTC().Format(time.RFC3339),
		Operation:    record.Operation,
	}
	raw, err := json.Marshal(canonical)
	if err != nil {
		return "invalid"
	}
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:])
}

func pairReplacementView(record *PairRecord) any {
	if record == nil {
		return nil
	}
	key, err := ssh.ParsePublicKey(record.HostKey)
	if err != nil {
		return nil
	}
	return map[string]string{"fingerprint": ssh.FingerprintSHA256(key), "registered_at": record.RegisteredAt.UTC().Format(time.RFC3339)}
}

func fingerprintPairKey(key ssh.PublicKey) string { return ssh.FingerprintSHA256(key) }
func fingerprintPairBytes(key []byte) string {
	parsed, err := ssh.ParsePublicKey(key)
	if err != nil {
		return ""
	}
	return ssh.FingerprintSHA256(parsed)
}

func writePairConsentPage(w http.ResponseWriter, view pairConsentPageView) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = pairConsentTemplate.Execute(w, view)
}

func setConsentPageHeaders(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; form-action 'self'; frame-ancestors 'none'")
}

type pairConsentPageView struct {
	Operation, FormToken, PrincipalLabel, Subject, HostLabel string
	Replacing                                                bool
	ReplacingLabel, ReplacingFingerprint, RegisteredAt       string
	NewFingerprint                                           string
}

var pairConsentTemplate = template.Must(template.New("pair-consent").Parse(`<!doctype html><html lang="ja"><head><meta charset="utf-8"><title>ホスト登録の確認</title></head><body><main><h1>ホスト登録の確認</h1><dl><dt>アカウント</dt><dd>{{.PrincipalLabel}}（{{.Subject}}）</dd><dt>登録枠</dt><dd>{{.HostLabel}}</dd><dt>置換される既存登録</dt><dd>{{if .Replacing}}{{.ReplacingFingerprint}}（{{.RegisteredAt}}）{{else}}{{.ReplacingLabel}}{{end}}</dd><dt>新しいホスト鍵</dt><dd>{{.NewFingerprint}}</dd></dl><form method="post" action="/tether-agent/v1/pair/consent"><input type="hidden" name="operation" value="{{.Operation}}"><input type="hidden" name="form_token" value="{{.FormToken}}"><button type="submit" name="decision" value="confirm">登録を確定</button><button type="submit" name="decision" value="cancel">取り消す</button></form></main></body></html>`))

const (
	pairConsentConfirmedHTML = `<!doctype html><html lang="ja"><head><meta charset="utf-8"><title>登録結果</title></head><body><main><p>登録を確定しました。アプリに戻ってください</p></main></body></html>`
	pairConsentCancelledHTML = `<!doctype html><html lang="ja"><head><meta charset="utf-8"><title>登録結果</title></head><body><main><p>登録を取り消しました。アプリに戻ってください</p></main></body></html>`
	pairConsentRejectedHTML  = `<!doctype html><html lang="ja"><head><meta charset="utf-8"><title>登録結果</title></head><body><main><p>登録を完了できませんでした。アプリに戻ってください</p></main></body></html>`
)

func writePairConsentResult(w http.ResponseWriter, status int, page string) {
	setConsentPageHeaders(w)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, page)
}

func writePairJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writePairError(w http.ResponseWriter, status int, code string) {
	writePairJSON(w, status, map[string]string{"error": code})
}
