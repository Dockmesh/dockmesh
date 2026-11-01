package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCommittedBundleIsCurrent(t *testing.T) {
	want, err := Bundle(filepath.Join("..", "..", "api", "openapi"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join("..", "..", "internal", "api", "openapi", "openapi.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatal("internal/api/openapi/openapi.yaml is out of date; run go run ./scripts/openapi and commit the result")
	}
}

func TestBundle(t *testing.T) {
	const root = "openapi: 3.1.0\npaths: {}\ncomponents:\n  schemas: {}\n  responses:\n    NotFound: {description: missing}\n"
	tests := []struct {
		name    string
		files   map[string]string
		want    []string // substrings of the bundle, in order
		wantErr string
	}{
		{
			name: "areas merge in file order",
			files: map[string]string{
				"b.yaml": "paths:\n  /b: {get: {summary: b}}\ncomponents:\n  schemas:\n    B: {type: string}\n",
				"a.yaml": "paths:\n  /a: {get: {summary: a}}\n",
			},
			want: []string{"openapi: 3.1.0", "paths:\n  /a:", "/b:", "schemas:\n    B:", "responses:"},
		},
		{
			name: "new component kind is appended",
			files: map[string]string{
				"a.yaml": "components:\n  parameters:\n    ID: {name: id, in: path}\n",
			},
			want: []string{"responses:", "parameters:\n    ID:"},
		},
		{
			name: "path defined twice",
			files: map[string]string{
				"a.yaml": "paths:\n  /x: {get: {summary: a}}\n",
				"b.yaml": "paths:\n  /x: {post: {summary: b}}\n",
			},
			wantErr: "b.yaml: paths./x is already defined in a.yaml",
		},
		{
			name:    "schema defined in root and area",
			files:   map[string]string{"a.yaml": "components:\n  responses:\n    NotFound: {description: again}\n"},
			wantErr: "components.responses.NotFound is already defined in root.yaml",
		},
		{
			name:    "other top-level key",
			files:   map[string]string{"a.yaml": "tags: [{name: a}]\n"},
			wantErr: `top-level key "tags" belongs in root.yaml`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			tc.files[rootFile] = root
			for name, body := range tc.files {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			b, err := Bundle(dir)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			out := string(b)
			for _, w := range tc.want {
				i := strings.Index(out, w)
				if i < 0 {
					t.Fatalf("bundle lacks %q in order:\n%s", w, string(b))
				}
				out = out[i+len(w):]
			}
		})
	}
}
