package main

import (
	"fmt"
	"io"
)

// No credential-consuming launcher is authorized by the protocol adapter.
// Reviewed real composition must replace this leaf, never a parser override.
func foreground(diagnostic io.Writer) int {
	fmt.Fprintln(diagnostic,"WORK_SOURCE_UNAVAILABLE: credential input and production trust are not configured")
	return 2
}
