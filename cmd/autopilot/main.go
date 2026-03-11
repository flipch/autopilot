package main

import (
	"fmt"
	"os"

	"github.com/felipeh/autopilot/internal/autopilot"
)

func main() {
	if err := autopilot.Run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
