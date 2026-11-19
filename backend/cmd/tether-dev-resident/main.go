//go:build tether_development

// tether-dev-resident is an explicitly untrusted local-development host.
// It is not the signed product, a service installer, or a trust override.
package main

import (
	"fmt"
	"io"
	"os"
)

const help = `Usage: tether-dev-resident --help | foreground
Development only: no Developer ID publisher validation. Not the production GUI.
Requires dedicated stdin/stdout pipes and separately approved dev enrollment.
No enrollment, key generation, login startup or daemon installation is provided.
Standby ends after its chosen hours, counted by elapsed and wall-clock time;
its end turns standby off and collects owned work like off.
This local limit is not an absolute access cutoff; a bounded server pilot is required.
Unknown cleanup is never success.
Commands: status; on <request_id> <expected_revision> <hours>;
extend <request_id> <expected_revision> <hours>; off <request_id> <expected_revision>;
close. <hours> is 2, 4, 8, 12 or 24; extend moves the end to that many hours
from now. Each ends in LF.
allow <request_id> <expected_revision> lets signed command runs through until
standby turns off; deny <request_id> <expected_revision> withdraws it.
Use fresh status revisions and strictly increasing positive request IDs.
ON alone permits only the fixed Tether BEGIN/sleep/END operation through MCP.
Receipts are not authentication, readiness or cleanup confirmation.
No policy, identity, command, endpoint, duration or trust override arguments.
`

func main() { os.Exit(execute(os.Args[1:], os.Stdout, os.Stderr, foreground)) }

func execute(args []string, output, diagnostic io.Writer, run func() int) int {
	if len(args) == 1 && args[0] == "--help" {
		if _, err := io.WriteString(output, help); err != nil {
			return 1
		}
		return 0
	}
	if len(args) != 1 || args[0] != "foreground" {
		fmt.Fprintln(diagnostic, "Usage: tether-dev-resident --help | foreground")
		return 2
	}
	if _, err := io.WriteString(diagnostic, "DEVELOPMENT ONLY: locally built, no Developer ID publisher validation; standby ends after its chosen hours like off; server-side expiry required.\n"); err != nil {
		return 1
	}
	return run()
}
