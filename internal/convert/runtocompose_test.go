package convert

import (
	"strings"
	"testing"
)

func TestRun_Simple(t *testing.T) {
	r, err := Run("docker run -d --name web -p 8080:80 nginx:alpine")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !strings.Contains(r.YAML, "services:") {
		t.Errorf("missing services key")
	}
	if !strings.Contains(r.YAML, "web:") {
		t.Errorf("missing web service key")
	}
	if !strings.Contains(r.YAML, "image: nginx:alpine") {
		t.Errorf("missing image: %s", r.YAML)
	}
	if !strings.Contains(r.YAML, "- 8080:80") {
		t.Errorf("missing port mapping: %s", r.YAML)
	}
	if !strings.Contains(r.YAML, "container_name: web") {
		t.Errorf("missing container_name: %s", r.YAML)
	}
}

func TestRun_Complex(t *testing.T) {
	r, err := Run(`docker run -d --name pg -e POSTGRES_USER=app -e POSTGRES_PASSWORD=secret -v pgdata:/var/lib/postgresql/data --restart unless-stopped -p 5432:5432 --network backend postgres:16`)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	checks := []string{
		"image: postgres:16",
		"POSTGRES_USER=app",
		"POSTGRES_PASSWORD=secret",
		"pgdata:/var/lib/postgresql/data",
		"restart: unless-stopped",
		"- backend",
		"- 5432:5432",
	}
	for _, want := range checks {
		if !strings.Contains(r.YAML, want) {
			t.Errorf("missing %q in:\n%s", want, r.YAML)
		}
	}
}

func TestRun_CombinedShortFlags(t *testing.T) {
	r, err := Run("docker run -it --rm alpine sh")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !strings.Contains(r.YAML, "stdin_open: true") {
		t.Errorf("missing stdin_open: %s", r.YAML)
	}
	if !strings.Contains(r.YAML, "tty: true") {
		t.Errorf("missing tty: %s", r.YAML)
	}
	if !strings.Contains(r.YAML, "- sh") {
		t.Errorf("missing command: %s", r.YAML)
	}
}

func TestRun_UnknownFlagWarning(t *testing.T) {
	r, err := Run("docker run --memory 512m nginx")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(r.Warnings) == 0 {
		t.Errorf("expected a warning for --memory")
	}
}

// TestRun_MultilineBackslash mirrors the real Vaultwarden install-docs
// command — copy-pasted with backslash line continuations. The parser
// must collapse those before shellwords sees the input or every flag
// after the first newline lands in the wrong slot.
func TestRun_MultilineBackslash(t *testing.T) {
	r, err := Run("docker run --detach --name vaultwarden \\\n" +
		"  --env DOMAIN=\"https://vw.domain.tld\" \\\n" +
		"  --volume /vw-data/:/data/ \\\n" +
		"  --restart unless-stopped \\\n" +
		"  --publish 127.0.0.1:8000:80 \\\n" +
		"  vaultwarden/server:latest")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	checks := []string{
		"image: vaultwarden/server:latest",
		"container_name: vaultwarden",
		"DOMAIN=https://vw.domain.tld",
		"- /vw-data/:/data/",
		"restart: unless-stopped",
		"- 127.0.0.1:8000:80",
	}
	for _, want := range checks {
		if !strings.Contains(r.YAML, want) {
			t.Errorf("missing %q in:\n%s", want, r.YAML)
		}
	}
	// Common mistake: the flags leaking into `command:` because the
	// parser stopped at the first backslash. Guard against regression.
	if strings.Contains(r.YAML, "command:") {
		t.Errorf("unexpected command: block — backslash collapse failed:\n%s", r.YAML)
	}
}

// TestRun_CRLF makes sure Windows-paste line endings also get normalised.
func TestRun_CRLF(t *testing.T) {
	r, err := Run("docker run \\\r\n  --name foo \\\r\n  nginx:alpine")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !strings.Contains(r.YAML, "image: nginx:alpine") {
		t.Errorf("CRLF line endings broke the parse:\n%s", r.YAML)
	}
}

func TestRun_ImageKeyFallback(t *testing.T) {
	r, err := Run("docker run ghcr.io/getdockmesh/dockmesh:latest")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !strings.Contains(r.YAML, "dockmesh:") {
		t.Errorf("expected image-derived key 'dockmesh', got:\n%s", r.YAML)
	}
}
