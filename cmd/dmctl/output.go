package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
)

// printTable renders a header + rows to stdout using tabwriter so
// columns align regardless of cell width.
func printTable(headers []string, rows [][]string) {
	tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, strings.Join(headers, "\t"))
	for _, r := range rows {
		fmt.Fprintln(tw, strings.Join(r, "\t"))
	}
	_ = tw.Flush()
}

// printResult is the output-format dispatcher every subcommand routes
// through. When --output json, it marshals and prints; when yaml,
// indented JSON (close enough for shell-friendly structured output
// without pulling in a YAML lib just for emit — callers can pipe to
// `yq` if they really need YAML).
//
// Table mode expects the caller to supply a tableFn that walks the
// structured data into headers + rows.
func printResult(data any, tableFn func() ([]string, [][]string)) error {
	switch flagOutput {
	case "json":
		return emitJSON(os.Stdout, data)
	case "yaml":
		return emitJSON(os.Stdout, data)
	case "table", "":
		if tableFn == nil {
			return emitJSON(os.Stdout, data)
		}
		h, rows := tableFn()
		printTable(h, rows)
		return nil
	default:
		return fmt.Errorf("unknown output format %q (expected: table | json | yaml)", flagOutput)
	}
}

func emitJSON(w io.Writer, data any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(data)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}
