package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"
)

func newLoginCmd() *cobra.Command {
	var tokenFlag string
	var userFlag string
	cmd := &cobra.Command{
		Use:   "login <server>",
		Short: "Save server URL + credentials to dmctl's config file",
		Long: `Records the server URL and credentials to ~/.config/dmctl/config.json (0600).

Two auth modes:

  1. API token (best for CI / scripts / long-lived scope):
       dmctl login https://dockmesh.example.com --token dmt_abc...
     Create one first under User Profile → API Tokens in the UI.

  2. Interactive username + password (best for day-to-day workstation use):
       dmctl login http://localhost:8080 --user admin
     Prompts for password (hidden). If MFA is enabled on the account,
     prompts for the TOTP code too. Gets a JWT pair — dmctl auto-refreshes
     the access token as it expires so sessions last as long as the
     refresh token (30 days by default).`,
		Example: `  # Interactive password login on the local server
  dmctl login http://localhost:8080 --user admin

  # API token flow — token via stdin (the safest non-interactive pattern)
  echo "dmt_abc123" | dmctl login https://dockmesh.example.com

  # Same, token as flag (visible in shell history — avoid on shared hosts)
  dmctl login https://dockmesh.example.com --token dmt_abc123`,
		Args: cobra.MatchAll(
			cobra.ExactArgs(1),
			func(cmd *cobra.Command, args []string) error {
				if args[0] == "" {
					return fmt.Errorf("server URL is required")
				}
				return nil
			},
		),
		// Custom error so the default cobra "accepts 1 arg(s), received 0"
		// noise is replaced with something actionable.
		SilenceUsage: false,
		RunE: func(cmd *cobra.Command, args []string) error {
			server := strings.TrimRight(args[0], "/")
			if !strings.HasPrefix(server, "http://") && !strings.HasPrefix(server, "https://") {
				// Default to https when scheme is missing. localhost /
				// *.local / 127.* override to http — those addresses
				// almost never have TLS, and guessing https would just
				// break the first login on someone's laptop.
				if looksLikePlainHTTPTarget(server) {
					server = "http://" + server
				} else {
					server = "https://" + server
				}
			}

			var (
				token        string
				refreshToken string
				err          error
			)

			switch {
			case userFlag != "":
				// Interactive password login path.
				token, refreshToken, err = interactivePasswordLogin(cmd, server, userFlag)
				if err != nil {
					return err
				}
			default:
				// API token path (legacy / CI).
				token, err = readToken(cmd, tokenFlag)
				if err != nil {
					return err
				}
			}

			cfg, _ := loadConfig()
			if cfg == nil {
				cfg = &Config{}
			}
			cfg.Server = server
			cfg.Token = token
			cfg.RefreshToken = refreshToken
			cfg.Insecure = flagInsecure
			if err := saveConfig(cfg); err != nil {
				return err
			}

			// Validate by calling /me. Gives immediate feedback instead of
			// the next real call failing with a confusing error later.
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
	cmd.Flags().StringVar(&tokenFlag, "token", "", "API token (default: read from stdin when --user is not given)")
	cmd.Flags().StringVar(&userFlag, "user", "", "Username for interactive password login (prompts for password + MFA)")
	return cmd
}

// readToken pulls an API token from --token, the TTY (with hidden
// input), or stdin (for pipe-feeding in CI).
func readToken(cmd *cobra.Command, flag string) (string, error) {
	if flag != "" {
		return flag, nil
	}
	if term.IsTerminal(int(os.Stdin.Fd())) {
		fmt.Fprint(cmd.ErrOrStderr(), "API token: ")
		raw, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(cmd.ErrOrStderr())
		if err != nil {
			return "", fmt.Errorf("read token: %w", err)
		}
		tok := strings.TrimSpace(string(raw))
		if tok == "" {
			return "", errors.New("token is required")
		}
		return tok, nil
	}
	s := bufio.NewScanner(os.Stdin)
	if !s.Scan() {
		return "", errors.New("token is required (pipe it to stdin or use --token / --user)")
	}
	tok := strings.TrimSpace(s.Text())
	if tok == "" {
		return "", errors.New("token is required")
	}
	return tok, nil
}

// interactivePasswordLogin drives the /auth/login → /auth/mfa flow.
// Returns an access token + refresh token on success.
func interactivePasswordLogin(cmd *cobra.Command, server, username string) (string, string, error) {
	var pw string
	if term.IsTerminal(int(os.Stdin.Fd())) {
		fmt.Fprintf(cmd.ErrOrStderr(), "Password for %s: ", username)
		raw, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(cmd.ErrOrStderr())
		if err != nil {
			return "", "", fmt.Errorf("read password: %w", err)
		}
		pw = string(raw)
	} else {
		s := bufio.NewScanner(os.Stdin)
		if s.Scan() {
			pw = s.Text()
		}
	}
	if pw == "" {
		return "", "", errors.New("password is required")
	}

	// Direct POST /auth/login without the Client wrapper — we don't have
	// a token yet.
	login, err := postJSON(server, "/api/v1/auth/login", map[string]string{
		"username": username,
		"password": pw,
	})
	if err != nil {
		return "", "", err
	}

	// MFA?
	if b, ok := login["mfa_required"].(bool); ok && b {
		mfaToken, _ := login["mfa_token"].(string)
		if mfaToken == "" {
			return "", "", errors.New("server requested MFA but returned no mfa_token")
		}
		code, err := promptTOTP(cmd)
		if err != nil {
			return "", "", err
		}
		mfa, err := postJSON(server, "/api/v1/auth/mfa", map[string]string{
			"mfa_token": mfaToken,
			"code":      code,
		})
		if err != nil {
			return "", "", err
		}
		login = mfa
	}

	at, _ := login["access_token"].(string)
	rt, _ := login["refresh_token"].(string)
	if at == "" {
		return "", "", errors.New("server returned no access_token")
	}
	return at, rt, nil
}

func promptTOTP(cmd *cobra.Command) (string, error) {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return "", errors.New("MFA required but stdin is not a terminal — pass TOTP code via script / CI token instead")
	}
	fmt.Fprint(cmd.ErrOrStderr(), "MFA code: ")
	r := bufio.NewReader(os.Stdin)
	code, err := r.ReadString('\n')
	if err != nil {
		return "", fmt.Errorf("read MFA code: %w", err)
	}
	code = strings.TrimSpace(code)
	if code == "" {
		return "", errors.New("MFA code is required")
	}
	return code, nil
}

// postJSON is a minimal one-off JSON POST used for the auth handshake
// before we have a full authenticated Client. Kept local to this file
// so the main Client wrapper stays single-purpose.
func postJSON(server, path string, body any) (map[string]any, error) {
	b, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	u, err := url.Parse(server + path)
	if err != nil {
		return nil, err
	}
	resp, err := authHTTPClient().Post(u.String(), "application/json", strings.NewReader(string(b)))
	if err != nil {
		return nil, fmt.Errorf("POST %s: %w", u, err)
	}
	defer resp.Body.Close()
	var out map[string]any
	raw, _ := readAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// Surface the server's error envelope when present for a nicer
		// "wrong password" / "MFA code rejected" UX than a raw HTTP code.
		var envelope struct{ Error string `json:"error"` }
		msg := string(raw)
		if json.Unmarshal(raw, &envelope) == nil && envelope.Error != "" {
			msg = envelope.Error
		}
		return nil, fmt.Errorf("%s → %d: %s", path, resp.StatusCode, trim(msg, 400))
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return out, nil
}

// looksLikePlainHTTPTarget returns true for hostnames that almost
// certainly aren't behind TLS. We use this to default to http:// when
// the user omits the scheme — saves one "ERR_SSL_PROTOCOL_ERROR" when
// logging into a homelab server for the first time.
func looksLikePlainHTTPTarget(server string) bool {
	host := server
	if i := strings.Index(host, "/"); i >= 0 {
		host = host[:i]
	}
	if i := strings.LastIndex(host, ":"); i >= 0 {
		host = host[:i]
	}
	switch host {
	case "localhost", "127.0.0.1", "::1":
		return true
	}
	return strings.HasSuffix(host, ".local") ||
		strings.HasSuffix(host, ".lan") ||
		strings.HasSuffix(host, ".internal") ||
		strings.HasPrefix(host, "192.168.") ||
		strings.HasPrefix(host, "10.") ||
		strings.HasPrefix(host, "172.")
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
			cfg.RefreshToken = ""
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
