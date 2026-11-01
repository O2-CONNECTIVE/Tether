# Authenticated static MCP discovery

The shared gateway accepts initialize without an attached remote host only after
its configured Access assertion or Bearer verifier and exact member-subject
mapping succeed. Every subsequent request is authenticated again. No anonymous
catalog route or cookie/alternate-auth fallback is added.

At initialize the session is fixed to exactly one of two states:

- An existing approved route: the existing immutable member/host/grant/epoch
  binding and all command rules remain unchanged.
- No route: a permanently unbound discovery-only session. It answers the static
  protocol handshake and four-tool schemas, never host inventory, live status,
  output, member identity, keys, approval metadata or grant details. Catalog
  descriptions of command parameters/permissions are static, not live facts.

A discovery session cannot acquire a backend later. A syntactically and
semantically valid tools/call ends that session with HTTP 404 and an instruction
to initialize a new session; nothing was dispatched. Invalid tool arguments and
premature calls retain the normal MCP parsing/lifecycle errors. After a new host
approval, only a new initialize can create a bound command session. An old bound
session that expired or was revoked is never converted into discovery or rebound.
Same request_id idempotency and payload-mismatch rules remain at the grant-bound
backend; discovery never stores or replays a command.

Both kinds of session use the same per-member cap and idle expiry, random session
IDs, principal isolation, DELETE and gateway shutdown rules. An offline catalog
is not permission to operate a machine. Gateway restart forgets all sessions.

The more convenient alternative of binding the first tools/call to a later
current grant is deliberately not implemented: it would allow an old discovery
request to acquire a later authority context. Reinitialize keeps that boundary
explicit. Whether the real dot MCP client automatically reinitializes after a
404 is unverified; a user may need to reconnect. No OAuth enrollment, redirect,
credential issuance, deployment, public route or real-client registration is
established by the source/fake tests described here.
