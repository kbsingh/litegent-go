// Command litegent is a lightweight, autonomous, unconfined CLI AI
// agent for Linux. See req/ for the full specification.
//
// This file is intentionally minimal: all logic lives in
// internal/cliapp so it remains unit-testable.
package main

import (
	"os"

	"litegent/internal/cliapp"
)

func main() {
	os.Exit(cliapp.Run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}
