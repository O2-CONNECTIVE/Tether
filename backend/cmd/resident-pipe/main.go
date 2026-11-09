// resident-pipe is the fixed-enrollment producer for a future verified native
// launch. It has no policy, credential, descriptor or transport override.
package main

import "os"

func main() { os.Exit(execute(os.Args[1:])) }

func execute(args []string) int {
	if !validArguments(args) {
		return 1 // Never claim the authoritative startup-rejected exit code.
	}
	return enrolled()
}

func validArguments(args []string) bool { return len(args) == 0 }
