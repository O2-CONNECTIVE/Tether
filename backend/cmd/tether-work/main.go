// tether-work is a narrow foreground client of an already scoped MCP session.
// Normal credential/trust composition is intentionally not configured.
package main

import (
	"fmt"
	"io"
	"os"
)

const help = `Usage: tether-work --help | foreground

One foreground session accepts dedicated stdin/stdout pipes, not a TTY.
Input is one exact LF-terminated command per line:
  start <request_id>
  result <request_id>
  cancel <request_id>
  release <request_id>
  list <-|cursor> <limit:1..32>
  get_execution <request_id>
  read_output <request_id> <cursor:0..9007199254740991> <max_bytes:1..4096>
  close
The existing work request_id is exactly 32 lowercase hexadecimal characters.
It is an idempotency key, not an RPC ID or a host ON/OFF request ID.
Only the existing fixed work is available. No command, profile, host or scope
may be selected. Result contains bounded base64 output as untrusted data.
Accepted work, terminal state, exit status and cleanup remain separate fields.
List is a live same-session view, not a snapshot; '-' starts at the empty cursor.
get_execution reads metadata without output. read_output is non-acknowledging;
gaps, released results and unknown states do not prove success or non-execution.
Pages are explicit: no automatic pagination or polling. Optional unavailable tools
return unsupported without dispatch. Discovery uses only the original session.
Release requires confirmed cleanup. Close abandons this MCP session only:
it does not cancel work, release a result, switch the host OFF or prove cleanup.
There is no automatic retry, token refresh, reconnection or generation reset.

Normal startup is unavailable: credential input and production trust are still
unsettled. No token, endpoint, CA, file, environment or approval override exists.
Only reviewed hosted-Mac CI composition supplies anonymous fixture bootstrap.
CI validates client/service/native integration, not production authentication.
`

func main() { os.Exit(execute(os.Args[1:], os.Stdout, os.Stderr)) }

func execute(args []string, output, diagnostic io.Writer) int {
	if len(args) == 1 && args[0] == "--help" {
		if _, e := io.WriteString(output, help); e != nil {
			return 1
		}
		return 0
	}
	if len(args) != 1 || args[0] != "foreground" {
		fmt.Fprintln(diagnostic, "Usage: tether-work --help | foreground")
		return 2
	}
	return foreground(diagnostic)
}
