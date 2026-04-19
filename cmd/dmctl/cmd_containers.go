package main

import (
	"fmt"
	"net/url"
	"os"
	"os/signal"
	"strings"

	"github.com/gorilla/websocket"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

func newContainersCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "containers", Short: "Container operations"}
	cmd.AddCommand(containersListCmd())
	cmd.AddCommand(containersLogsCmd())
	cmd.AddCommand(containersExecCmd())
	return cmd
}

type containerListEntry struct {
	ID     string            `json:"id"`
	Names  []string          `json:"names"`
	Image  string            `json:"image"`
	State  string            `json:"state"`
	Status string            `json:"status"`
	Labels map[string]string `json:"labels,omitempty"`
	HostID string            `json:"host_id,omitempty"`
}

func containersListCmd() *cobra.Command {
	var all bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List containers on a host (or all hosts)",
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := newClient()
			if err != nil {
				return err
			}
			q := c.withHost(url.Values{})
			if all {
				q.Set("all", "1")
			}
			var cs []containerListEntry
			if err := c.request("GET", "/api/v1/containers", q, nil, &cs); err != nil {
				return err
			}
			return printResult(cs, func() ([]string, [][]string) {
				rows := make([][]string, 0, len(cs))
				for _, c := range cs {
					name := ""
					if len(c.Names) > 0 {
						name = strings.TrimPrefix(c.Names[0], "/")
					}
					rows = append(rows, []string{
						truncate(c.ID, 12),
						name,
						truncate(c.Image, 40),
						c.State,
						truncate(c.Status, 30),
					})
				}
				return []string{"ID", "NAME", "IMAGE", "STATE", "STATUS"}, rows
			})
		},
	}
	cmd.Flags().BoolVarP(&all, "all", "a", false, "Include stopped containers")
	return cmd
}

func containersLogsCmd() *cobra.Command {
	var tail string
	var follow bool
	cmd := &cobra.Command{
		Use:   "logs <container-id>",
		Short: "Stream a container's logs",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := newClient()
			if err != nil {
				return err
			}
			return streamContainerLogs(c, args[0], tail, follow, "")
		},
	}
	cmd.Flags().StringVar(&tail, "tail", "100", "Lines of history to show before following")
	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "Follow log output")
	return cmd
}

func containersExecCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:                "exec <container-id> [-- <cmd> [args...]]",
		Short:              "Start an interactive shell / run a command inside a container",
		Args:               cobra.MinimumNArgs(1),
		DisableFlagParsing: false,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Everything after `--` is the command to run. Default is /bin/sh.
			containerID := args[0]
			rest := args[1:]
			cmdStr := "/bin/sh"
			if len(rest) > 0 {
				cmdStr = strings.Join(rest, " ")
			}
			c, err := newClient()
			if err != nil {
				return err
			}
			return runExec(c, containerID, cmdStr)
		},
	}
	return cmd
}

// runExec opens the WS exec session and wires stdin / stdout / window
// resize between the local terminal and the remote container.
//
// Protocol mirrors the web UI:
//   - BinaryMessage client→server = stdin bytes
//   - BinaryMessage server→client = stdout/stderr (TTY merged)
//   - TextMessage   client→server = JSON {"type":"resize","cols":N,"rows":M}
func runExec(c *Client, containerID, cmdStr string) error {
	ticket, err := c.wsTicket()
	if err != nil {
		return err
	}
	q := url.Values{}
	q.Set("ticket", ticket)
	q.Set("cmd", cmdStr)
	if flagHost != "" {
		q.Set("host", flagHost)
	}
	u := c.wsURL("/api/v1/ws/exec/"+url.PathEscape(containerID), q)

	conn, resp, err := c.wsDialer().Dial(u, nil)
	if err != nil {
		if resp != nil {
			return fmt.Errorf("ws dial: %d", resp.StatusCode)
		}
		return fmt.Errorf("ws dial: %w", err)
	}
	defer conn.Close()

	// Put the local terminal into raw mode so Ctrl-C, Ctrl-D, arrow
	// keys, and the like land in the remote shell instead of being
	// handled locally.
	stdinFD := int(os.Stdin.Fd())
	var restore func()
	if term.IsTerminal(stdinFD) {
		oldState, err := term.MakeRaw(stdinFD)
		if err == nil {
			restore = func() { _ = term.Restore(stdinFD, oldState) }
			defer restore()
		}
		// Initial window size.
		if cols, rows, err := term.GetSize(stdinFD); err == nil {
			if b, err := marshalResize(uint16(cols), uint16(rows)); err == nil {
				_ = conn.WriteMessage(websocket.TextMessage, b)
			}
		}
	}

	// Resize notifier: some Unices send SIGWINCH, which we forward as a
	// resize control frame. Not available on Windows — skip silently.
	sigResize := make(chan os.Signal, 1)
	watchResize(sigResize)
	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-done:
				return
			case <-sigResize:
				if cols, rows, err := term.GetSize(stdinFD); err == nil {
					if b, err := marshalResize(uint16(cols), uint16(rows)); err == nil {
						_ = conn.WriteMessage(websocket.TextMessage, b)
					}
				}
			}
		}
	}()
	defer close(done)

	// Ctrl-C forwarding is implicit in raw mode: the ^C byte goes onto
	// stdin and we ship it to the server like any other keystroke. But
	// we still catch SIGINT to clean up on dmctl getting killed.
	sigInt := make(chan os.Signal, 1)
	signal.Notify(sigInt, os.Interrupt)
	defer signal.Stop(sigInt)
	go func() {
		<-sigInt
		_ = conn.Close()
	}()

	// stdout: WS → local terminal
	errOut := make(chan error, 1)
	go func() {
		for {
			_, msg, err := conn.ReadMessage()
			if err != nil {
				if websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
					errOut <- nil
					return
				}
				errOut <- err
				return
			}
			_, _ = os.Stdout.Write(msg)
		}
	}()

	// stdin: local terminal → WS
	buf := make([]byte, 1024)
	for {
		n, err := os.Stdin.Read(buf)
		if n > 0 {
			if werr := conn.WriteMessage(websocket.BinaryMessage, buf[:n]); werr != nil {
				return werr
			}
		}
		if err != nil {
			// EOF on stdin (Ctrl-D in cooked mode) — let the server close
			// the session cleanly.
			break
		}
	}
	return <-errOut
}

