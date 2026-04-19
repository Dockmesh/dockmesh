package main

import (
	"fmt"
	"net/url"

	"github.com/spf13/cobra"
)

func newAlertCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "alert", Short: "Alert rules + history"}
	cmd.AddCommand(alertListActiveCmd())
	cmd.AddCommand(alertHistoryCmd())
	cmd.AddCommand(alertRulesCmd())
	return cmd
}

type alertHistory struct {
	ID            int64   `json:"id"`
	RuleID        int64   `json:"rule_id"`
	RuleName      string  `json:"rule_name"`
	ContainerName string  `json:"container_name"`
	Status        string  `json:"status"`
	Message       string  `json:"message"`
	Value         float64 `json:"value"`
	Threshold     float64 `json:"threshold"`
	OccurredAt    string  `json:"occurred_at"`
}

type alertRule struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	Metric    string `json:"metric"`
	Threshold float64 `json:"threshold"`
	Duration  int    `json:"duration_seconds"`
	Enabled   bool   `json:"enabled"`
}

// alertListActiveCmd implements `dmctl alert list-active` from the spec.
// "Active" = most recent `firing` history entries per rule. We pull the
// history list and filter client-side since the server doesn't expose a
// dedicated active-alerts endpoint.
func alertListActiveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list-active",
		Short: "Alerts currently in firing state",
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := newClient()
			if err != nil {
				return err
			}
			q := url.Values{}
			q.Set("limit", "500")
			var hist []alertHistory
			if err := c.request("GET", "/api/v1/alerts/history", q, nil, &hist); err != nil {
				return err
			}
			// Keep the newest status per rule; a rule whose latest entry is
			// "firing" is active, everything else is resolved / never fired.
			latest := map[int64]alertHistory{}
			for _, h := range hist {
				prev, seen := latest[h.RuleID]
				if !seen || h.OccurredAt > prev.OccurredAt {
					latest[h.RuleID] = h
				}
			}
			active := make([]alertHistory, 0)
			for _, h := range latest {
				if h.Status == "firing" {
					active = append(active, h)
				}
			}
			return printResult(active, func() ([]string, [][]string) {
				rows := make([][]string, 0, len(active))
				for _, a := range active {
					rows = append(rows, []string{
						fmt.Sprint(a.RuleID),
						a.RuleName,
						a.ContainerName,
						fmt.Sprintf("%.2f / %.2f", a.Value, a.Threshold),
						a.OccurredAt,
						truncate(a.Message, 40),
					})
				}
				return []string{"RULE", "NAME", "CONTAINER", "VALUE/THRESHOLD", "SINCE", "MESSAGE"}, rows
			})
		},
	}
}

func alertHistoryCmd() *cobra.Command {
	var limit int
	cmd := &cobra.Command{
		Use:   "history",
		Short: "Full alert history (most recent first)",
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := newClient()
			if err != nil {
				return err
			}
			q := url.Values{}
			q.Set("limit", fmt.Sprint(limit))
			var hist []alertHistory
			if err := c.request("GET", "/api/v1/alerts/history", q, nil, &hist); err != nil {
				return err
			}
			return printResult(hist, func() ([]string, [][]string) {
				rows := make([][]string, 0, len(hist))
				for _, h := range hist {
					rows = append(rows, []string{
						h.OccurredAt, h.Status, h.RuleName, h.ContainerName, truncate(h.Message, 40),
					})
				}
				return []string{"TIME", "STATUS", "RULE", "CONTAINER", "MESSAGE"}, rows
			})
		},
	}
	cmd.Flags().IntVar(&limit, "limit", 100, "Max entries to return")
	return cmd
}

func alertRulesCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "rules",
		Short: "List configured alert rules",
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := newClient()
			if err != nil {
				return err
			}
			var rules []alertRule
			if err := c.request("GET", "/api/v1/alerts/rules", nil, nil, &rules); err != nil {
				return err
			}
			return printResult(rules, func() ([]string, [][]string) {
				rows := make([][]string, 0, len(rules))
				for _, r := range rules {
					en := "no"
					if r.Enabled {
						en = "yes"
					}
					rows = append(rows, []string{
						fmt.Sprint(r.ID), r.Name, r.Metric,
						fmt.Sprintf("%.2f", r.Threshold),
						fmt.Sprint(r.Duration) + "s",
						en,
					})
				}
				return []string{"ID", "NAME", "METRIC", "THRESHOLD", "DURATION", "ENABLED"}, rows
			})
		},
	}
}
