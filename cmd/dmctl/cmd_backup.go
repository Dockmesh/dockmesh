package main

import (
	"fmt"
	"net/url"

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
	EndedAt   string `json:"ended_at,omitempty"`
	BytesOut  int64  `json:"bytes_out"`
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
	return &cobra.Command{
		Use:   "run <job-name-or-id>",
		Short: "Trigger a backup job immediately",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := newClient()
			if err != nil {
				return err
			}
			// Resolve name → id if the user passed a name. Small number of
			// jobs makes the extra round-trip cheap and the UX "just works"
			// without forcing operators to remember numeric IDs.
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
			var out map[string]any
			if err := c.request("POST", "/api/v1/backups/jobs/"+url.PathEscape(id)+"/run", nil, nil, &out); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Backup started: run_id=%v\n", out["run_id"])
			return nil
		},
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
						fmt.Sprint(r.BytesOut),
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
