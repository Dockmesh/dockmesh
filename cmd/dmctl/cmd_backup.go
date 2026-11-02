package main

import (
	"context"
	"fmt"
	"net/url"
	"time"

	"github.com/spf13/cobra"
)

func newBackupCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "backup", Short: "Backup job operations"}
	cmd.AddCommand(backupJobsListCmd())
	cmd.AddCommand(backupRunCmd())
	cmd.AddCommand(backupRunsListCmd())
	return cmd
}

type backupJob struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Type     string `json:"type"`
	Schedule string `json:"schedule"`
	Enabled  bool   `json:"enabled"`
	TargetID int64  `json:"target_id"`
}

type backupRun struct {
	ID        int64  `json:"id"`
	JobID     int64  `json:"job_id"`
	Status    string `json:"status"`
	StartedAt string `json:"started_at"`
	EndedAt   string `json:"finished_at,omitempty"`
	SizeBytes int64  `json:"size_bytes"`
	Error     string `json:"error,omitempty"`
}

func backupJobsListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "jobs",
		Short: "List backup jobs",
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := newClient()
			if err != nil {
				return err
			}
			var jobs []backupJob
			if err := c.request("GET", "/api/v1/backups/jobs", nil, nil, &jobs); err != nil {
				return err
			}
			return printResult(jobs, func() ([]string, [][]string) {
				rows := make([][]string, 0, len(jobs))
				for _, j := range jobs {
					en := "no"
					if j.Enabled {
						en = "yes"
					}
					rows = append(rows, []string{
						fmt.Sprint(j.ID), j.Name, j.Type, j.Schedule, en,
					})
				}
				return []string{"ID", "NAME", "TYPE", "SCHEDULE", "ENABLED"}, rows
			})
		},
	}
}

func backupRunCmd() *cobra.Command {
	var noWait bool
	cmd := &cobra.Command{
		Use:   "run <job-name-or-id>",
		Short: "Run a backup job now and wait until it finishes",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := newClient()
			if err != nil {
				return err
			}
			id := args[0]
			if !isNumeric(id) {
				var jobs []backupJob
				if err := c.request("GET", "/api/v1/backups/jobs", nil, nil, &jobs); err != nil {
					return err
				}
				found := ""
				for _, j := range jobs {
					if j.Name == args[0] {
						found = fmt.Sprint(j.ID)
						break
					}
				}
				if found == "" {
					return fmt.Errorf("no backup job named %q", args[0])
				}
				id = found
			}
			var run backupRun
			if err := c.request("POST", "/api/v1/backups/jobs/"+url.PathEscape(id)+"/run", nil, nil, &run); err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if noWait {
				fmt.Fprintf(out, "Backup run %d started\n", run.ID)
				return nil
			}
			fmt.Fprintf(out, "Backup run %d started, waiting for it to finish\n", run.ID)
			done, err := waitBackupRun(cmd.Context(), c, run.ID, 2*time.Second)
			if err != nil {
				return err
			}
			fmt.Fprintf(out, "Backup run %d: %s, %d bytes\n", done.ID, done.Status, done.SizeBytes)
			if done.Error != "" {
				fmt.Fprintf(out, "Error: %s\n", done.Error)
			}
			if done.Status == "failed" {
				return fmt.Errorf("backup run %d failed", done.ID)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&noWait, "no-wait", false, "Return once the run has started")
	return cmd
}

// waitBackupRun polls a run until it is no longer running. Polling
// survives a few failed requests in a row, such as a server restart
// during a long backup.
func waitBackupRun(ctx context.Context, c *Client, runID int64, every time.Duration) (*backupRun, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	const maxFailures = 5
	failures := 0
	path := fmt.Sprintf("/api/v1/backups/runs/%d", runID)
	for {
		var run backupRun
		err := c.request("GET", path, nil, nil, &run)
		switch {
		case err != nil:
			failures++
			if failures >= maxFailures {
				return nil, fmt.Errorf("waiting for backup run %d: %w", runID, err)
			}
		case run.Status != "running":
			return &run, nil
		default:
			failures = 0
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(every):
		}
	}
}

func backupRunsListCmd() *cobra.Command {
	var limit int
	cmd := &cobra.Command{
		Use:   "runs",
		Short: "Recent backup runs",
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := newClient()
			if err != nil {
				return err
			}
			q := url.Values{}
			q.Set("limit", fmt.Sprint(limit))
			var runs []backupRun
			if err := c.request("GET", "/api/v1/backups/runs", q, nil, &runs); err != nil {
				return err
			}
			return printResult(runs, func() ([]string, [][]string) {
				rows := make([][]string, 0, len(runs))
				for _, r := range runs {
					rows = append(rows, []string{
						fmt.Sprint(r.ID),
						fmt.Sprint(r.JobID),
						r.Status,
						r.StartedAt,
						fmt.Sprint(r.SizeBytes),
						truncate(r.Error, 40),
					})
				}
				return []string{"RUN", "JOB", "STATUS", "STARTED", "BYTES", "ERROR"}, rows
			})
		},
	}
	cmd.Flags().IntVar(&limit, "limit", 20, "Max runs to return")
	return cmd
}

func isNumeric(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}
