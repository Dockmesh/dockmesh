package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"
)

func newLoginCmd() *cobra.Command {
	var tokenFlag string
	cmd := &cobra.Command{
		Use:   "login <server>",
		Short: "Save server URL + API token to dmctl's config file",
		Long: `Records a server URL and API token to ~/.config/dmctl/config.json (0600).
If --token is omitted, reads the token from stdin without echoing.

Create an API token first under Settings → API Tokens in the UI (P.11.1).`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			server := strings.TrimRight(args[0], "/")
			if !strings.HasPrefix(server, "http://") && !strings.HasPrefix(server, "https://") {
				server = "https://" + server
			}
			token := tokenFlag
			if token == "" {
				// Read from stdin silently when stdin is a TTY; otherwise
				// accept whatever the caller piped (CI-friendly).
				if term.IsTerminal(int(os.Stdin.Fd())) {
					fmt.Fprint(cmd.ErrOrStderr(), "API token: ")
					raw, err := term.ReadPassword(int(os.Stdin.Fd()))
					fmt.Fprintln(cmd.ErrOrStderr())
					if err != nil {
						return fmt.Errorf("read token: %w", err)
					}
					token = strings.TrimSpace(string(raw))
				} else {
					s := bufio.NewScanner(os.Stdin)
					if s.Scan() {
						token = strings.TrimSpace(s.Text())
					}
				}
			}
			if token == "" {
				return fmt.Errorf("token is required (use --token or pipe it to stdin)")
			}

			cfg, _ := loadConfig()
			if cfg == nil {
				cfg = &Config{}
			}
			cfg.Server = server
			cfg.Token = token
			cfg.Insecure = flagInsecure
			if err := saveConfig(cfg); err != nil {
				return err
			}

			// Validate by calling a cheap authenticated endpoint. Gives
			// immediate feedback instead of waiting for the next real call
			// to fail with a confusing error.
			flagServer = server
			flagToken = token
			c, err := newClient()
			if err != nil {
				return err
			}
			var me map[string]any
			if err := c.request("GET", "/api/v1/me", nil, nil, &me); err != nil {
				return fmt.Errorf("credentials saved, but server rejected them: %w", err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Logged in to %s as %v\n", server, firstString(me, "email", "username", "id"))
			return nil
		},
	}
	cmd.Flags().StringVar(&tokenFlag, "token", "", "API token (default: read from stdin)")
	return cmd
}

func newLogoutCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "logout",
		Short: "Clear the saved token (keeps the server URL)",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			cfg.Token = ""
			if err := saveConfig(cfg); err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "Logged out.")
			return nil
		},
	}
}

func newWhoamiCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "whoami",
		Short: "Print the authenticated user",
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := newClient()
			if err != nil {
				return err
			}
			var me map[string]any
			if err := c.request("GET", "/api/v1/me", nil, nil, &me); err != nil {
				return err
			}
			return printResult(me, func() ([]string, [][]string) {
				return []string{"ID", "EMAIL", "ROLE"},
					[][]string{{
						fmt.Sprint(me["id"]),
						fmt.Sprint(firstString(me, "email", "username")),
						fmt.Sprint(me["role"]),
					}}
			})
		},
	}
}

// firstString picks the first non-empty string-valued field in m.
// Used to soft-degrade across user-shape differences (some endpoints
// return email, some username, some both).
func firstString(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k]; ok {
			if s, ok := v.(string); ok && s != "" {
				return s
			}
		}
	}
	return ""
}
