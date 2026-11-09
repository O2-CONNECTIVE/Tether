package main

import (
	"fmt"
	"io"
)

// No loader, environment option or path may turn unestablished provenance into
// authority. A future sealed composition belongs here, not in the CLI parser.
func foreground(diagnostic io.Writer) int {
	fmt.Fprintln(diagnostic, "RESIDENT_SOURCE_UNAVAILABLE: sealed enrollment and publisher/image trust are not configured")
	return 2
}
