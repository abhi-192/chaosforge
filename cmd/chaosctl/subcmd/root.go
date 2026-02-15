package subcmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

func NewRoot() *cobra.Command {
	cmd := &cobra.Command{
		Use:           "chaosctl",
		Short:         "ChaosForge operational CLI (validate)",
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			_ = cmd.Help()
			return fmt.Errorf("no command provided")
		},
	}

	cmd.SetOut(os.Stdout)
	cmd.SetErr(os.Stderr)

	cmd.AddCommand(NewValidateCmd())
	return cmd
}
