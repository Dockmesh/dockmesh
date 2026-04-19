package main

import (
	"github.com/dockmesh/dockmesh/pkg/version"
	"github.com/spf13/cobra"
)

// Root-level flags resolved before any subcommand's RunE. Populated by
// persistent PersistentPreRunE which walks flag → env → config in
// that priority order.
var (
	flagServer   string
	flagToken    string
	flagHost     string
	flagInsecure bool
	flagOutput   string
)

func newRootCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:           "dmctl",
		Short:         "Dockmesh command-line client",
		Long:          "dmctl — the scriptable Dockmesh client. Talks to a running Dockmesh server via its REST API using a scoped API token.",
		Version:       version.Version,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	cmd.PersistentFlags().StringVar(&flagServer, "server", "", "Dockmesh server URL (env: DMCTL_SERVER)")
	cmd.PersistentFlags().StringVar(&flagToken, "token", "", "API token (env: DMCTL_TOKEN)")
	cmd.PersistentFlags().StringVar(&flagHost, "host", "", "Target host ID for container/stack operations (default: server's local docker)")
	cmd.PersistentFlags().BoolVar(&flagInsecure, "insecure", false, "Skip TLS verification — only for local / self-signed dev servers")
	cmd.PersistentFlags().StringVarP(&flagOutput, "output", "o", "table", "Output format: table | json | yaml")

	cmd.AddCommand(newLoginCmd())
	cmd.AddCommand(newLogoutCmd())
	cmd.AddCommand(newWhoamiCmd())
	cmd.AddCommand(newStacksCmd())
	cmd.AddCommand(newContainersCmd())
	cmd.AddCommand(newBackupCmd())
	cmd.AddCommand(newAlertCmd())
	cmd.AddCommand(newEnrollCmd())
	return cmd
}
