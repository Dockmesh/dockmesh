package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

// newEnrollCmd creates the `dmctl enroll agent` subcommand. The flow
// mirrors the UI's "New agent" dialog: POST /agents with a name,
// receive the one-shot token + install hint, print them for the
// operator to paste onto the target host.
//
// We deliberately DON'T invoke the installer script on the local
// machine here — dmctl may be running nowhere near the host that
// needs the agent. Print the command; let the operator run it where
// it belongs.
func newEnrollCmd() *cobra.Command {
	var name string
	cmd := &cobra.Command{
		Use:   "enroll",
		Short: "Create a new agent enrollment token + install command",
	}
	agentCmd := &cobra.Command{
		Use:   "agent",
		Short: "Create a new agent (prints the install command to run on the target host)",
		RunE: func(cmd *cobra.Command, args []string) error {
			if name == "" {
				return fmt.Errorf("--name is required")
			}
			c, err := newClient()
			if err != nil {
				return err
			}
			type createReq struct {
				Name string `json:"name"`
			}
			var res map[string]any
			if err := c.request("POST", "/api/v1/agents", nil, createReq{Name: name}, &res); err != nil {
				return err
			}
			if flagOutput == "json" || flagOutput == "yaml" {
				return printResult(res, nil)
			}
			fmt.Fprintln(cmd.OutOrStdout(), "Agent created.")
			if hint, ok := res["install_hint"].(string); ok && hint != "" {
				fmt.Fprintln(cmd.OutOrStdout())
				fmt.Fprintln(cmd.OutOrStdout(), "Run this on the target host as root:")
				fmt.Fprintln(cmd.OutOrStdout())
				fmt.Fprintln(cmd.OutOrStdout(), "  "+hint)
				fmt.Fprintln(cmd.OutOrStdout())
				fmt.Fprintln(cmd.OutOrStdout(), "The enrollment token is valid for a single use and ~15 minutes.")
			} else {
				// Fall back to showing the raw JSON so nothing is lost.
				return printResult(res, nil)
			}
			return nil
		},
	}
	agentCmd.Flags().StringVar(&name, "name", "", "Display name for the new agent (required)")
	cmd.AddCommand(agentCmd)
	return cmd
}
