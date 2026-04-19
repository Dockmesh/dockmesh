package main

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/spf13/cobra"
)

func newStacksCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "stacks", Short: "Stack operations"}
	cmd.AddCommand(stacksListCmd())
	cmd.AddCommand(stacksGetCmd())
	cmd.AddCommand(stacksDeployCmd())
	cmd.AddCommand(stacksStopCmd())
	cmd.AddCommand(stacksStatusCmd())
	cmd.AddCommand(stacksLogsCmd())
	return cmd
}

type stackListEntry struct {
	Name       string            `json:"name"`
	Deployment *stackDeployment  `json:"deployment,omitempty"`
	Services   []stackStatusLine `json:"-"` // populated for status command, not list
}

type stackDeployment struct {
	HostID   string `json:"host_id"`
	HostName string `json:"host_name,omitempty"`
	Status   string `json:"status"`
}

type stackStatusLine struct {
	Service     string `json:"service"`
	ContainerID string `json:"container_id"`
	State       string `json:"state"`
	Status      string `json:"status"`
	Image       string `json:"image"`
}

func stacksListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List stacks",
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := newClient()
			if err != nil {
				return err
			}
			var stacks []stackListEntry
			if err := c.request("GET", "/api/v1/stacks", nil, nil, &stacks); err != nil {
				return err
			}
			return printResult(stacks, func() ([]string, [][]string) {
				rows := make([][]string, 0, len(stacks))
				for _, s := range stacks {
					host, status := "-", "-"
					if s.Deployment != nil {
						if s.Deployment.HostName != "" {
							host = s.Deployment.HostName
						} else {
							host = s.Deployment.HostID
						}
						status = s.Deployment.Status
					}
					rows = append(rows, []string{s.Name, host, status})
				}
				return []string{"NAME", "HOST", "STATUS"}, rows
			})
		},
	}
}

func stacksGetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "get <name>",
		Short: "Print a stack's compose.yaml + env",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := newClient()
			if err != nil {
				return err
			}
			var detail map[string]any
			if err := c.request("GET", "/api/v1/stacks/"+url.PathEscape(args[0]), nil, nil, &detail); err != nil {
				return err
			}
			return printResult(detail, func() ([]string, [][]string) {
				// Table mode prints YAML content directly — no grid layout
				// makes sense for a multi-line document.
				if v, ok := detail["compose"].(string); ok {
					fmt.Println("# compose.yaml")
					fmt.Println(v)
				}
				if v, ok := detail["env"].(string); ok && v != "" {
					fmt.Println("\n# .env")
					fmt.Println(v)
				}
				return nil, nil
			})
		},
	}
}

func stacksDeployCmd() *cobra.Command {
	var env string
	cmd := &cobra.Command{
		Use:   "deploy <name>",
		Short: "Deploy a stack (resolves dependencies + environment overlay)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := newClient()
			if err != nil {
				return err
			}
			q := c.withHost(url.Values{})
			if env != "" {
				if q == nil {
					q = url.Values{}
				}
				q.Set("environment", env)
			}
			var res map[string]any
			if err := c.request("POST", "/api/v1/stacks/"+url.PathEscape(args[0])+"/deploy", q, nil, &res); err != nil {
				return err
			}
			return printResult(res, func() ([]string, [][]string) {
				fmt.Fprintf(cmd.OutOrStdout(), "Deployed %s\n", args[0])
				if deps, ok := res["dependencies_deployed"].([]any); ok && len(deps) > 0 {
					fmt.Fprintf(cmd.OutOrStdout(), "  Auto-deployed dependencies: %v\n", deps)
				}
				if svcs, ok := res["services"].([]any); ok {
					fmt.Fprintf(cmd.OutOrStdout(), "  %d service(s)\n", len(svcs))
				}
				return nil, nil
			})
		},
	}
	cmd.Flags().StringVar(&env, "environment", "", "Compose overlay name (compose.<name>.yaml)")
	return cmd
}

func stacksStopCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "stop <name>",
		Short: "Stop a deployed stack",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := newClient()
			if err != nil {
				return err
			}
			if err := c.request("POST", "/api/v1/stacks/"+url.PathEscape(args[0])+"/stop", c.withHost(nil), nil, nil); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Stopped %s\n", args[0])
			return nil
		},
	}
}

func stacksStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status <name>",
		Short: "Per-service status of a deployed stack",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := newClient()
			if err != nil {
				return err
			}
			var lines []stackStatusLine
			if err := c.request("GET", "/api/v1/stacks/"+url.PathEscape(args[0])+"/status", c.withHost(nil), nil, &lines); err != nil {
				return err
			}
			return printResult(lines, func() ([]string, [][]string) {
				rows := make([][]string, 0, len(lines))
				for _, l := range lines {
					rows = append(rows, []string{l.Service, l.State, l.Status, truncate(l.Image, 40), truncate(l.ContainerID, 12)})
				}
				return []string{"SERVICE", "STATE", "STATUS", "IMAGE", "CONTAINER"}, rows
			})
		},
	}
}

func stacksLogsCmd() *cobra.Command {
	var tail string
	var follow bool
	var service string
	cmd := &cobra.Command{
		Use:   "logs <name>",
		Short: "Stream logs from a stack's services (first container per service)",
		Long: `Streams logs from one container per service in the stack. When --service
is specified, only that service is followed; otherwise all services are
interleaved with a '[service]' prefix per line.

Use --no-follow for a one-shot dump of recent lines.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := newClient()
			if err != nil {
				return err
			}
			var lines []stackStatusLine
			if err := c.request("GET", "/api/v1/stacks/"+url.PathEscape(args[0])+"/status", c.withHost(nil), nil, &lines); err != nil {
				return err
			}
			if len(lines) == 0 {
				return fmt.Errorf("stack %q has no running containers", args[0])
			}
			// Keep the first container per service — the UX here matches
			// `docker compose logs` rather than "every replica, every time".
			seen := map[string]bool{}
			var targets []stackStatusLine
			for _, l := range lines {
				if service != "" && l.Service != service {
					continue
				}
				if seen[l.Service] {
					continue
				}
				seen[l.Service] = true
				targets = append(targets, l)
			}
			if len(targets) == 0 {
				return fmt.Errorf("no matching service on stack %q", args[0])
			}

			// Multiplex WS streams onto stdout with per-service prefix.
			prefixFor := func(svc string) string {
				if len(targets) == 1 {
					return ""
				}
				return "[" + svc + "] "
			}
			errCh := make(chan error, len(targets))
			for _, t := range targets {
				t := t
				go func() {
					errCh <- streamContainerLogs(c, t.ContainerID, tail, follow, prefixFor(t.Service))
				}()
			}
			// Wait for all streams to finish; in follow mode they finish on
			// Ctrl-C or container exit, which streamContainerLogs surfaces
			// as a nil error.
			var firstErr error
			for range targets {
				if err := <-errCh; err != nil && firstErr == nil {
					firstErr = err
				}
			}
			// Reserved for later: richer exit codes per-service.
			_ = strings.Builder{}
			return firstErr
		},
	}
	cmd.Flags().StringVar(&tail, "tail", "100", "Lines of history to show before following")
	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "Follow log output (Ctrl-C to stop)")
	cmd.Flags().StringVar(&service, "service", "", "Restrict to a single service (default: all services in stack)")
	return cmd
}
