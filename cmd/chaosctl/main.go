package main

import (
	"fmt"
	"os"

	"chaosforge/cmd/chaosctl/subcmd"
)

func main() {
	if err := subcmd.NewRoot().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
