# Tether: local Access assertion identity inspector

This document describes the implemented inspector contract, not an authorization or a record of enrollment. Current validation scope and outstanding work are recorded in [pilot status](tether-pilot-status.md). See [gateway authentication](agent-gateway.md) for the Access verifier contract.

## Purpose and non-authority

`backend/cmd/tether-access-identity` is a one-time enrollment aid. It shows the verified `(issuer, sub)` of one Cloudflare Access application assertion so that an operator can transcribe that subject into the gateway's protected `principals` through the existing independently checked change path.

It grants nothing. It is not a command authorization path and never a fallback for the gateway: it registers no principal, writes no configuration, and its output is not accepted as input anywhere. Only `subject` is an enrollment value. `email` is display-only, for a human to recognize whose subject this is; it never identifies or authorizes anyone, and an email-only principal is refused by the gateway configuration.

## Interface

```
tether-access-identity --issuer <https://team-domain> --aud <AUD tag> --jwks-json '<{"keys":[...]}>' < one-assertion
```

- Public inputs, all required, each exactly once as `--name value`: `--issuer` (exact team-domain origin), `--aud` (the Access application AUD tag), `--jwks-json` (the literal public JWK Set JSON, i.e. the `keys` member of the team certs document wrapped as `{"keys":[...]}`; not a path). They are the same trust inputs as the gateway's `access_issuer`, `access_aud` and `access_keys` and are built by the existing `agentgateway.NewAccessVerifier` with a fixed 30-second leeway. Command-line values are visible to other local processes; only public values belong there.
- The only secret input is exactly one compact assertion on stdin, optionally followed by one `\n`. Empty input, a CR, whitespace, a second line or token, or trailing data is refused. Input is bounded to `agentgateway.MaxTokenBytes` plus the newline and must end with EOF within 30 seconds; on the deadline stdin is closed and nothing is printed.
- There is no positional argument, `=` form, token flag, environment read, file read or write, network access, subprocess, token issuance or automatic registration. Flags and trust inputs are fully validated before stdin is read.

Verification is `agentgateway.AccessVerifier.Verify`: RS256 against the configured kid only, optional header `typ` (JWT when present), payload `type` app, exact issuer, AUD listed in an `aud` array, integer `exp`/`iat`/`nbf` within bounds, non-empty user `sub`, `email` present, no `common_name`. Nothing is printed unless it succeeds. Because the verified identity omits email, only after success is the payload of that same verified token decoded once more to read `email`.

On success, exit 0 and one JSON line with exactly these members:

```json
{"verified":true,"issuer":"…","subject":"…","expected_aud":"…","email":"…","expires_at":"RFC 3339 UTC"}
```

`expected_aud` is the configured AUD tag that the assertion was verified to list. No raw token, header, other claim, key material or error text is printed.

Failures print one fixed message on stderr and nothing on stdout: exit 2 for usage or invalid trust inputs (stdin untouched), exit 1 for an input, deadline, verification or output failure.

## Operator invocation and handling boundary

```
cloudflared access login --quiet https://<exact-app-url>
cloudflared access token -app=https://<exact-app-url> | tether-access-identity --issuer … --aud … --jwks-json '…'
```

The second command's output goes only into the pipe. Do not run `cloudflared access token` on its own in a shell, agent tool or session whose output is captured, logged or shared. Confirm these flags against the installed official cloudflared version before use. Installation and interactive login require separate explicit authorization.

## Secret handling limits

- The pipe is not memory-only handling. `cloudflared access login` stores the application token, and possibly an organization token, in its local cache files (mode 0600) under the user's profile; they persist after this inspector exits and remain usable until they expire. Remove or let them expire according to cloudflared's own documentation; this tool does not manage them.
- The assertion passes through kernel pipe buffers and both processes' memory. The inspector clears its mutable read and decode buffers best effort. Go strings are immutable and the runtime may copy data, so the token string and copies cannot be reliably zeroized.
- A valid, unexpired assertion is a bearer credential for the Access application. Treat any captured copy as a secret until it expires.
- The inspector checks the same signature and claims as the gateway, but enrollment remains an operator decision; a verified subject is not a policy approval.

## Verification gaps

- Not built, formatted or tested by the author; the coordinator runs gofmt, build, vet and tests.
- `scripts/go_ci.py` keeps a per-package source inventory; this new package (`main.go`) is not added to it here, by scope.
- Synthetic in-memory RSA keys and assertions only. The actual Access assertion members, certs `alg` presence and cloudflared output framing (for example a CRLF terminator, which is refused) are unverified.
- Stdin deadline and close behavior is tested with fakes only. A blocking native stdin may not return from `Read` on close; the reader is then abandoned and ends with the process.
- No real issuer, AUD, team, key, subject or email is recorded here or in the tests.

Access JWT compatibility: RFC 7519 section 5.1 makes JOSE `typ` optional. Access mode permits absence only; present empty, null, non-string or non-JWT values are rejected. OAuth mode still requires its configured type. Signature and every Access claim check remain mandatory; original signed bytes are unchanged.
