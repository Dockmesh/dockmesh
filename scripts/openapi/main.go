// Command openapi bundles the OpenAPI sources under api/openapi/ into the
// single internal/api/openapi/openapi.yaml the server embeds. root.yaml
// holds everything but paths and schemas; each other file holds the paths
// of one tag and the schemas only that tag uses, common.yaml the shared
// schemas.
//
//	go run ./scripts/openapi          rewrite the bundle
//	go run ./scripts/openapi -check   fail if the bundle is out of date
package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"
)

func main() {
	src := flag.String("src", "api/openapi", "source directory")
	out := flag.String("out", "internal/api/openapi/openapi.yaml", "bundled spec")
	check := flag.Bool("check", false, "compare instead of writing")
	flag.Parse()
	if err := run(*src, *out, *check); err != nil {
		fmt.Fprintln(os.Stderr, "openapi:", err)
		os.Exit(1)
	}
}

func run(src, out string, check bool) error {
	b, err := Bundle(src)
	if err != nil {
		return err
	}
	if !check {
		return os.WriteFile(out, b, 0o644) //nolint:gosec // committed source file
	}
	cur, err := os.ReadFile(out) //nolint:gosec // path from the CLI caller
	if err != nil {
		return err
	}
	if !bytes.Equal(cur, b) {
		return fmt.Errorf("%s is out of date; run go run ./scripts/openapi and commit the result", out)
	}
	return nil
}
