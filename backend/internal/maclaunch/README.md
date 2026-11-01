# Recipient-local Mac approval owner

This is source implementation, not live approval, enrollment or deployment.
The real ApproveLocal path ran under own PTY with synthetic input, public fixture
keys and a runtime that cannot start processes. This is not human approval,
real-key CLI startup or remote command execution. Fake-terminal tests do not prove
that a controlling terminal belongs to a human or that native process cleanup works.

## Concrete path

Recipient-owned composition supplies an existing host signer, already enrolled
client public keys, local cwd/HOME, an absolute deadline and finite budgets to
Prepare. This validates macagent.New without starting a command and prepares
one ephemeral current-user challenge; no Authority exists yet.

Pending.ApproveLocal consumes that proposal once and opens only /dev/tty on
Darwin. It refuses root or mismatched real/effective UID/GID. There is no stdin,
MCP, relay, environment or noninteractive approval fallback. It displays all key
fingerprints, the fixed deadline and cwd/HOME, unrestricted current-user access,
no isolation and the scoped cleanup limitation. The user must type the fresh
proposal's exact phrase. Rejection, EOF, oversized input, cancellation or expiry
issues no grant; a new attempt requires a fresh proposal and fresh confirmation.
The owned tty is opened nonblocking and uses bounded select readiness, not
Go os.File deadlines. Unsupported descriptors refuse before the prompt.
Repeated interrupted/spurious syscalls are capped and refuse. Reads and writes recheck context
and the absolute wall deadline every 100 ms; a context deadline also caps the
initial elapsed budget fixed at Prepare. A backward wall observation before
confirmation refuses. The original monotonic deadline is also passed into
agentexec.Config.ElapsedDeadline and can only shorten its budget; approval
cannot replenish time already spent at the prompt. Every request rechecks that
elapsed cap even if its timer callback is delayed. The successful owner follows the caller lifetime, not
the short-lived prompt context; returning from the prompt does not revoke it. No terminal modes or inherited fd flags are changed.
On the tested host, own-PTY native smoke covers read/write, cancellation, expiry,
concurrent Close and stopped-output Close.
This does not substitute for real user confirmation or the remote command demo.
The OS close syscall itself is not promised a portable timeout.

After confirmation the single owner calls the prepared issuer's Launch,
agentexec.New and agentlink.NewHost. The policy is fixed MacCurrentUserPolicyDigest;
request fields cannot select a policy or replace the canonical Authority.
The returned Approved owner provides public Dispatch metadata and Serve(ctx,conn).
Serve uses that exact Session and the enrolled-key channel, then revokes and
waits for owned cleanup. Close retries only the same owner's unconfirmed cleanup.

Dispatch contains binding, deadline and a copied host public key, not a bearer
credential. The client still proves the approved private key. Authentic delivery
of dispatch metadata and the opaque connection are not supplied here.

## Trust and remaining work

The recipient must explicitly start the trusted package and control its terminal.
A compromised same-user process or terminal injector is outside this current-user
boundary; this is not a security boundary against the current user's own malware.
There is no promise that cwd confines files, that commands cannot reach current-user
secrets/network, or that detached descendants/crash recovery are reclaimed.

Operational relay registration and connection supply, authentic dispatch delivery,
package launcher composition, client signer selection and dots MCP registration
remain unimplemented. No executable default is enabled and the old product gate
is unchanged. No keys, credentials, services or persistent settings are created.

The terminal prompt labels the waiting state, remaining time at display, exact
confirmation string and final input position in Japanese. Public fingerprints
are explicitly not private keys. The original deadline is not extended while
reading the explanation. Expiry/cancellation/EOF diagnostics are fixed text;
raw errors and typed input are never printed. The CLI still needs a foreground
controlling terminal and exact human confirmation for an actual session.
