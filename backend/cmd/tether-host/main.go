// tether-host is a pipe-driven foreground adapter, not an interactive terminal
// or an execution client. Normal trusted startup remains deliberately unbound.
package main

import (
	"fmt"
	"io"
	"os"
)

const help = `Usage: tether-host --help | foreground

The foreground host adapter accepts dedicated stdin/stdout pipes, not a TTY.
Input is one exact LF-terminated command per line:
  status
  on <request_id> <expected_revision>
  off <request_id> <expected_revision>
  close
Output is the shared JSONL status / receipt / closed protocol. Use the latest
fresh status revision and strictly increasing request IDs. An accepted receipt
does not confirm ON/OFF or cleanup. Close is permanent; later input is ignored.
No execution, result-read or cancel commands are provided; those remain MCP tools.

Normal startup is unavailable: sealed enrollment and publisher/image trust are
not configured. There is no policy, key, CA, approval or trust override flag.
Only the reviewed hosted-Mac CI fixture supplies synthetic startup composition.
CI proves that instrumented path, not production authentication or distribution.
`

func main() { os.Exit(execute(os.Args[1:], os.Stdout, os.Stderr)) }

func execute(args []string, output, diagnostic io.Writer) int {
	if len(args) == 1 && args[0] == "--help" {
		if _, err := io.WriteString(output, help); err != nil {
			return 1
		}
		return 0
	}
	if len(args) != 1 || args[0] != "foreground" {
		fmt.Fprintln(diagnostic, "Usage: tether-host --help | foreground")
		return 2
	}
	return foreground(diagnostic)
}
