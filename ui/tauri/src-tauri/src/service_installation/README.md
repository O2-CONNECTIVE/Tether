# Installation source pairing and inactive native evaluator

This candidate does not connect the GUI to a service or enable execution. The normal `get_shell_status` caller polls the private Installation Module; its fixed production gate returns NotConfigured even when generated seed source is present. There is no environment, argument, webview or settings override. ShellStatus remains unwired and both operation flags remain false. The existing control/1 Windows client is unchanged.

## One provisioning input, two generated sources

The offline Go CLI `tether-service-client-manifest` preserves its existing projection/check modes and adds:

1. `--anchor-source INPUT`: validate canonical reviewed Anchor input and emit Go Anchor source.
2. Build the selected service externally from that Anchor source. This command does not build or install it.
3. `--paired-anchor INPUT --selected-go-anchor SOURCE --record RECORD --service-image BUILD`: require the same canonical Go source, strict exact Record and actual selected build-byte hash; emit an untrusted source-pair JSON containing Go Anchor source and Rust seed source.

The existing Go fixedEvaluationConfig actually calls the generated Anchor function; ordinary source returns the zero Anchor and all other required runtime budgets remain unset. The Rust constructor consumes the generated seed function, whose ordinary source returns None, then independently enforces the closed evaluator gate. Generated source does not bypass that gate.

Build order is acyclic: Anchor source has no Record/service hash; service bytes precede Record; Record and expected digest enter Rust source. Source/build pairing does not attest which source produced a binary or prove protected installation. The blocked package checker is not an installer. Real provisioning remains a separate responsibility.

This is only a host-specific evaluation-source format, not an adopted general distribution mechanism. Configured Rust source would embed bounded exact nonsecret Record bytes, including deployment SIDs/paths. Distribution requires explicit metadata review. Public fixtures are synthetic; no real deployment input or host-specific build is included. Changes require rebuilding and coordinated replacement. There is no atomic installer here; mixed expected Record/digest/source pairs refuse rather than refreshing or accepting observed values as expected values.

## Inactive native candidate

The Windows adapter source holds token, ancestor and leaf handles, verifies fixed volume/path/file IDs and effective owner/ACL, and compares protected bytes against generated exact bytes. Only SYSTEM, Administrators and the looked-up service SID may have effective mutation rights. Operator read access does not imply write, delete, WRITE_DAC or WRITE_OWNER; access denial never causes elevation or permission changes. Go Root's service-role requirement is unchanged. Matching bytes are not the authenticity proof: held protection and independently supplied compiled seed are separate requirements. Existing leaked writable handles/mappings are not disproved by current ACL inspection.

One private worker retains all acquisition resources. Polling is nonblocking, requests are bounded and coalesced, and Checked(sequence) is a one-shot historical check result, never current service/approval authority. Failure is irreversible; no reacquisition. There is no SCM, pipe, consent, grant or agent connection.

Pending OVERLAPPED state/buffer/event/file cannot be freed during unwind. Raw native operation ownership and the worker panic guard preserve resources until observed completion or fail-stop. Synthetic tests exercise the same internal IO loop and worker guard, not OS behavior.

**Unresolved availability policy:** the inactive candidate aborts the whole GUI process on uncertain close/pending completion or failed worker join. Its current two-second Drop cutoff can precede a longer configured cleanup budget, losing unrelated UI state. This policy is NOT accepted for product use, NOT evidence of successful cleanup, and never stops the service. A retained app-lifetime Unknown owner is an alternative, but nonreturning inline OS calls can leave an unbounded worker. No new supervisor or permission is introduced to hide that tradeoff. The fixed gate must remain closed until policy, implementation and native evidence are reviewed together.

## Evidence limitations

Go producer tests use finite synthetic readers. Rust fixtures, formatting, lint, compilation and Windows execution have not been run locally because the Rust toolchain is absent. Existing hosted Windows `tauri-shell.yml` fmt/clippy/test/build is a future verification path only after publication approval. No dependency, feature or workflow is added. Passing those pure tests would still not establish actual installation, native cleanup or consent guarantees.
