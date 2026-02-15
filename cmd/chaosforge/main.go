package main

import (
	"context"
	"os"

	"chaosforge/cmd/chaosforge/subcmd"
)

func main() {
	ctx := context.Background()
	root := subcmd.NewRoot()
	if err := root.ExecuteContext(ctx); err != nil {
		os.Exit(1)
	}
}
