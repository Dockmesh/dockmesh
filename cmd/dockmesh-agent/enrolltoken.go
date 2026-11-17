package main

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// enrollTokenFile is the one-time token the installer leaves in the data
// dir. It is removed once the server has answered it, so no secret stays
// on disk after enrollment.
const enrollTokenFile = "enroll-token"

// enrollTokenPattern matches the tokens the server issues; the installer
// checks the same pattern.
var enrollTokenPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{16,128}$`)

// errNotEnrolled is a host without a certificate and without a token:
// nothing the agent can retry.
var errNotEnrolled = errors.New("this host is not enrolled")

// notEnrolledHint is the journal line for errNotEnrolled.
const notEnrolledHint = "This host is not enrolled. Copy the install command from Hosts > Add host in Dockmesh and run it on this host."

type enrollPaths struct {
	cert, key, ca, url, token string
}

func dataDirEnrollPaths(dir string) enrollPaths {
	return enrollPaths{
		cert:  filepath.Join(dir, "agent.crt"),
		key:   filepath.Join(dir, "agent.key"),
		ca:    filepath.Join(dir, "ca.crt"),
		url:   filepath.Join(dir, "agent.url"),
		token: filepath.Join(dir, enrollTokenFile),
	}
}

func (p enrollPaths) enrolled() bool {
	return fileExists(p.cert) && fileExists(p.key) && fileExists(p.ca)
}

// readEnrollToken returns the token in path, empty when there is none.
// A symlink, a non-regular file or a malformed token is an error.
func readEnrollToken(path string) (string, error) {
	lfi, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if !lfi.Mode().IsRegular() {
		return "", fmt.Errorf("%s is not a regular file", path)
	}
	f, err := os.Open(path) // #nosec G304 -- fixed name in the agent's data dir
	if err != nil {
		return "", err
	}
	defer f.Close()
	// The same file as the one checked: a swap for a symlink in between
	// is refused.
	if fi, err := f.Stat(); err != nil || !os.SameFile(lfi, fi) {
		return "", fmt.Errorf("%s changed while it was read", path)
	}
	b, err := io.ReadAll(io.LimitReader(f, 4096))
	if err != nil {
		return "", err
	}
	tok := strings.TrimSpace(string(b))
	if !enrollTokenPattern.MatchString(tok) {
		return "", fmt.Errorf("%s does not hold an enrollment token", path)
	}
	return tok, nil
}

func removeEnrollToken(path string) {
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		slog.Warn("could not remove the enrollment token file", "path", path, "err", err)
	}
}

// enrollOnStart enrolls a host without a certificate, with the token
// file or else DOCKMESH_TOKEN, and re-enrolls an enrolled host when a
// token file is present. A failed re-enrollment keeps the previous
// certificate. The token file is removed once the server has accepted or
// refused it; after a network error it stays for the next start.
// errNotEnrolled and errEnrollRejected mean a restart cannot help.
func enrollOnStart(cfg agentConfig, p enrollPaths, enrollFn func(agentConfig, enrollPaths) error) error {
	fileToken, err := readEnrollToken(p.token)
	if err != nil {
		slog.Error("enrollment token file ignored", "err", err)
		removeEnrollToken(p.token)
		fileToken = ""
	}

	if !p.enrolled() {
		token := fileToken
		if token == "" {
			token = cfg.token
		}
		if cfg.enrollURL == "" || token == "" {
			slog.Error(notEnrolledHint, "missing", missingEnrollSettings(cfg.enrollURL, token))
			return errNotEnrolled
		}
		cfg.token = token
		slog.Info("enrolling", "url", cfg.enrollURL)
		err := enrollFn(cfg, p)
		if err == nil || errors.Is(err, errEnrollRejected) {
			removeEnrollToken(p.token)
		}
		if err != nil {
			if errors.Is(err, errEnrollRejected) {
				slog.Error("enrollment failed", "err", err, "hint", reenrollHint)
			} else {
				slog.Error("enrollment failed", "err", err)
			}
			return err
		}
		slog.Info("enrolled successfully")
		return nil
	}

	if fileToken == "" {
		return nil
	}
	if cfg.enrollURL == "" {
		slog.Error("re-enrollment skipped: DOCKMESH_ENROLL_URL is not set; the agent keeps its previous enrollment")
		return nil
	}
	cfg.token = fileToken
	slog.Info("re-enrolling", "url", cfg.enrollURL)
	err = enrollFn(cfg, p)
	switch {
	case err == nil:
		removeEnrollToken(p.token)
		slog.Info("re-enrolled with a new client certificate")
	case errors.Is(err, errEnrollRejected):
		removeEnrollToken(p.token)
		slog.Error("re-enrollment failed", "err", err,
			"hint", "The new token was not accepted. The agent keeps its previous enrollment.")
	default:
		slog.Error("re-enrollment failed", "err", err,
			"hint", "The agent keeps its previous enrollment and tries the token again at the next start.")
	}
	return nil
}

func missingEnrollSettings(enrollURL, token string) string {
	var m []string
	if enrollURL == "" {
		m = append(m, "DOCKMESH_ENROLL_URL")
	}
	if token == "" {
		m = append(m, "enrollment token")
	}
	return strings.Join(m, ", ")
}
