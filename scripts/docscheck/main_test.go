package main

import (
	"os"
	"reflect"
	"testing"
	"testing/fstest"
)

func TestRepositoryDocs(t *testing.T) {
	problems, err := check(os.DirFS("../../docs/site"))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range problems {
		t.Error(p)
	}
}

const page = "---\ntitle: T\ndescription: D\n---\n\nBody.\n"

func TestCheck(t *testing.T) {
	tests := []struct {
		name  string
		files fstest.MapFS
		want  []string
	}{
		{
			name: "valid",
			files: fstest.MapFS{
				"nav.json":    {Data: []byte(`{"version":1,"sections":[{"title":"A","items":[{"title":"X","path":"a/x.md"},{"title":"Y","path":"a/y.md"}]}]}`)},
				"README.md":   {Data: []byte("# not a page\n")},
				"a/x.md":      {Data: []byte("---\r\ntitle: X\r\ndescription: \"Has: colon\"\r\n---\r\n\r\nSee [y](./y.md#part), [site](https://example.com), [top](#top), `[no](./nope.md)`.\r\n\r\n```\r\n[also](./nope.md)\r\n```\r\n")},
				"a/y.md":      {Data: []byte(page)},
				"assets/i.md": {Data: []byte("not a page")},
			},
		},
		{
			name: "unlisted, duplicate and missing",
			files: fstest.MapFS{
				"nav.json": {Data: []byte(`{"version":1,"sections":[{"title":"A","items":[{"title":"X","path":"a/x.md"},{"title":"X2","path":"a/x.md"},{"title":"G","path":"a/gone.md"}]}]}`)},
				"a/x.md":   {Data: []byte(page)},
				"a/z.md":   {Data: []byte(page)},
			},
			want: []string{
				"a/x.md: listed 2 times in nav.json",
				"a/z.md: not listed in nav.json",
				"nav.json: a/gone.md does not exist",
			},
		},
		{
			name: "bad frontmatter and links",
			files: fstest.MapFS{
				"nav.json": {Data: []byte(`{"version":2,"sections":[{"title":"A","items":[{"title":"X","path":"a/x.md"},{"title":"","path":"../y.md"}]}]}`)},
				"a/x.md":   {Data: []byte("---\ntitle: X\nsidebar: 3\n---\n\n[a](../b.md) [c](/abs.md) [d](../../out.md)\n")},
			},
			want: []string{
				"nav.json: version 2, want 1",
				"nav.json: ../y.md: item without title",
				`nav.json: invalid path "../y.md"`,
				"a/x.md: frontmatter description missing",
				`a/x.md: unexpected frontmatter key "sidebar"`,
				"a/x.md: link ../b.md: target does not exist",
				"a/x.md: link /abs.md: absolute link, use a relative path",
				"a/x.md: link ../../out.md: points outside docs/site",
			},
		},
		{
			name: "missing frontmatter",
			files: fstest.MapFS{
				"nav.json": {Data: []byte(`{"version":1,"sections":[{"title":"A","items":[{"title":"X","path":"x.md"}]}]}`)},
				"x.md":     {Data: []byte("# Title\n")},
			},
			want: []string{"x.md: no frontmatter"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := check(tt.files)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("got\n%q\nwant\n%q", got, tt.want)
			}
		})
	}
}
