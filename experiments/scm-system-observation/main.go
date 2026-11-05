package main

import "os"

func main() {
	if len(os.Args) == 2 && os.Args[1] == "--summary" {
		os.Exit(RunSummary(os.Stdin, os.Stdout))
	}
	os.Exit(RunCLI(os.Args[1:], runNative))
}

// RunCLI permits only the fixed SCM mode; it has no install or configuration mode.
func RunCLI(args []string, service func() int) int {
	if len(args) != 1 || args[0] != "--service" {
		return 2
	}
	return service()
}
