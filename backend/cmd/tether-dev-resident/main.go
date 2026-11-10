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
The original run requests shutdown after 15 minutes, including reconnections.
This timer is not an absolute access cutoff; a bounded server pilot is required.
Expiry requests original-owner cleanup; unknown cleanup is never success.
Commands: status; on <request_id> <expected_revision>;
off <request_id> <expected_revision>; close. Each ends in LF.
Use fresh status revisions and strictly increasing positive request IDs.
ON permits only the existing fixed Tether BEGIN/sleep/END operation through MCP.
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
	if _, err := io.WriteString(diagnostic, "DEVELOPMENT ONLY: locally built, no Developer ID publisher validation; 15-minute shutdown-request timer; server-side expiry required.\n"); err != nil {
		return 1
	}
	return run()
}
