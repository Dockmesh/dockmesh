package proxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPrepareCaddyfile(t *testing.T) {
	const common = "(common) {\n\tencode gzip\n}\n"
	tests := []struct {
		name string
		text string
		ok   bool
		want []string // expected in the expanded text
	}{
		{"plain", "a.example.com {\n\treverse_proxy b:80\n}\n", true, []string{"reverse_proxy b:80"}},
		{"snippet import", common + "a.example.com {\n\timport common\n\treverse_proxy b:80\n}\n", true, []string{"encode gzip", "reverse_proxy b:80"}},
		{"quoted snippet name", common + "a.example.com {\n\timport \"common\"\n}\n", true, []string{"encode gzip"}},
		{"import on the block line", common + "a.example.com { import common }\n", true, []string{"encode gzip"}},
		{"one-line snippet", "(c) { encode gzip }\na.example.com {\n\timport c\n}\n", true, []string{"encode gzip"}},
		{"snippet with a whole site", "(site) {\n\ta.example.com {\n\t\treverse_proxy b:80\n\t}\n}\nimport site\n", true, []string{"a.example.com {"}},
		{"nested snippets", common + "(edge) {\n\timport common\n\theader -Server\n}\na.example.com {\n\timport edge\n}\n", true, []string{"encode gzip", "header -Server"}},
		{"nested snippet defined before the use", "(edge) {\n\timport common\n}\n" + common + "a.example.com {\n\timport edge\n}\n", true, []string{"encode gzip"}},
		{"crlf and bom", "\uFEFF(common) {\r\n\tencode gzip\r\n}\r\na.example.com {\r\n\timport common\r\n}\r\n", true, []string{"encode gzip"}},
		{"carriage return inside import", "imp\rort /etc/passwd\n", false, nil},
		{"carriage return inside a site", "a.example.com {\n\timp\rort /etc/passwd\n}\n", false, nil},
		{"carriage return inside client_auth", "a.example.com {\n\ttls {\n\t\tclient\r_auth {\n\t\t\ttrusted_ca_cert\r_file /etc/passwd\n\t\t}\n\t}\n}\n", false, nil},
		{"two byte order marks", "\xef\xbb\xbf\xef\xbb\xbfimport /etc/passwd\n", false, nil},
		{"byte order mark inside import", "a.example.com {\n\t\xef\xbb\xbfimport /etc/passwd\n}\n", false, nil},
		{"unused snippet is dropped", "(x) {\n\timport /etc/passwd\n}\na.example.com {\n}\n", true, nil},
		{"snippet defined after the import", "a.example.com {\n\timport common\n}\n" + common, false, nil},
		{"path named like a later snippet", "a.example.com {\n\timport /etc/passwd\n}\n(/etc/passwd) {\n}\n", false, nil},
		{"file import", "a.example.com {\n\timport /data/caddy/certificates/x.key\n}\n", false, nil},
		{"glob import", "import /etc/*\n", false, nil},
		{"path import on the block line", "a.example.com { import /config/caddy/autosave.json }\n", false, nil},
		{"path import in a snippet", "(x) {\n\timport /etc/passwd\n}\na.example.com {\n\timport x\n}\n", false, nil},
		{"import continued on the next line", "a.example.com {\n\timport \\\n\t\t/etc/passwd\n}\n", false, nil},
		{"quoted import", "a.example.com {\n\t\"import\" /etc/passwd\n}\n", false, nil},
		{"import with arguments", common + "a.example.com {\n\timport common x\n}\n", false, nil},
		{"import with a block", common + "a.example.com {\n\timport common {\n\t\tx y\n\t}\n}\n", false, nil},
		{"snippet using arguments", "(s) {\n\t{args[0]}{args[1]} /etc/passwd\n}\na.example.com {\n\timport s\n}\n", false, nil},
		{"snippet importing itself", "(a) {\n\timport a\n}\nx.example.com {\n\timport a\n}\n", false, nil},
		{"snippet defined twice", common + common, false, nil},
		{"too many imports", common + "a.example.com {\n" + strings.Repeat("\timport common\n", maxSnippetImports+1) + "}\n", false, nil},
		{"client ca file in a snippet", "(m) {\n\ttls {\n\t\tclient_auth {\n\t\t\ttrusted_ca_cert_file /etc/passwd\n\t\t}\n\t}\n}\na.example.com {\n\timport m\n}\n", false, nil},
		{"env substitution in a snippet", "(u) {\n\treverse_proxy {$UPSTREAM}\n}\na.example.com {\n\timport u\n}\n", false, nil},
		{"too large", strings.Repeat("#", MaxCaddyfileSize+1), false, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := prepareCaddyfile(tt.text)
			if (err == nil) != tt.ok {
				t.Fatalf("err = %v, want ok=%v; expanded:\n%s", err, tt.ok, got)
			}
			if err != nil {
				if !errors.Is(err, ErrCaddyfileRejected) {
					t.Fatalf("err = %v is not ErrCaddyfileRejected", err)
				}
				return
			}
			for _, f := range strings.Fields(got) {
				if f == "import" || strings.HasPrefix(f, "(") {
					t.Fatalf("expanded text still has %q:\n%s", f, got)
				}
			}
			for _, w := range tt.want {
				if !strings.Contains(got, w) {
					t.Errorf("expanded text lacks %q:\n%s", w, got)
				}
			}
		})
	}
}

func TestLexCaddyfile(t *testing.T) {
	text := "a {\n\t\"q \\\" x\" `b t` # c\n\tk \\\n\t\tv\n\trespond <<EOF\n\t\timport x\n\t\tEOF 200\n\t\\<<x\n}\n"
	var got []string
	for _, tok := range lexCaddyfile(text) {
		got = append(got, fmt.Sprintf("%d:%s", tok.line, tok.text))
	}
	want := []string{"1:a", "1:{", `2:q " x`, "2:b t", "3:k", "3:v", "4:respond", "4:\t\timport x\n\t\t", "6:200", "7:<<x", "8:}"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("tokens = %q\nwant     %q", got, want)
	}
}

func TestImportCaddyfileSnippets(t *testing.T) {
	var posted []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		posted = append(posted, string(body))
		_, _ = w.Write([]byte(`{"result":{},"warnings":[{"line":4,"message":"Caddyfile input is not formatted; run 'caddy fmt --overwrite' to fix inconsistencies"}]}`))
	}))
	old := adminURL
	adminURL = srv.URL
	t.Cleanup(func() { adminURL = old; srv.Close() })
	s := newServiceDB(t)
	s.enabled.Store(true)
	ctx := context.Background()

	res, err := s.ImportCaddyfile(ctx, "(common) {\n\tencode gzip\n}\na.example.com {\n\timport common\n\treverse_proxy b:80\n}\n", true)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Warnings) != 0 {
		t.Fatalf("formatting warning of the expanded text kept: %v", res.Warnings)
	}
	if len(posted) != 1 || strings.Contains(posted[0], "import") || strings.Contains(posted[0], "(common)") || !strings.Contains(posted[0], "encode gzip") {
		t.Fatalf("sent to caddy: %q", posted)
	}
	if _, err := s.ImportCaddyfile(ctx, "a.example.com {\n\timport /etc/caddy/*.key\n}\n", true); !errors.Is(err, ErrCaddyfileRejected) {
		t.Fatalf("path import: err = %v", err)
	}
	if len(posted) != 1 {
		t.Fatalf("path import reached caddy: %q", posted[1:])
	}
}
