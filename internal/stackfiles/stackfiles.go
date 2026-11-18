// Package stackfiles carries the files next to a stack's compose.yaml from
// the server to an agent: Collect reads them from the server's stack
// directory, Apply writes them into the agent's copy.
//
// The agent's stack directory is shared with the stack's containers, which
// may write there too. Apply therefore only replaces or removes a file that
// is still exactly what Dockmesh wrote before (recorded in a Manifest kept
// outside that directory); everything else is left alone and reported.
package stackfiles

import (
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Limits of one file set. A set travels inside a single agent frame
// (agents.MaxFrameBytes); MaxEncodedBytes bounds its JSON size, base64
// data and escaped paths included, so compose and .env still fit.
const (
	MaxTotalBytes   = 8 << 20
	MaxEncodedBytes = 12 << 20
	MaxEntries      = 2000
	MaxPathBytes    = 1024
)

// encodedSize is an upper bound of one entry's share of the JSON frame:
// base64 data, the path with every byte escaped as \u00XX, and the keys.
func encodedSize(p string, dataLen int64) int64 {
	return int64(base64.StdEncoding.EncodedLen(int(dataLen))) + 6*int64(len(p)) + 64
}

// File is one entry of a set. Path is slash-separated and relative to the
// stack directory; Mode holds permission bits only.
type File struct {
	Path string `json:"path"`
	Mode uint32 `json:"mode"`
	Dir  bool   `json:"dir,omitempty"`
	Data []byte `json:"data,omitempty"`
}

// Set is the content of a stack directory as the server has it.
type Set struct {
	Files []File `json:"files"`
}

// Size is the number of data bytes in the set.
func (s *Set) Size() int64 {
	var n int64
	for _, f := range s.Files {
		n += int64(len(f.Data))
	}
	return n
}

// Count is the number of regular files in the set.
func (s *Set) Count() int {
	n := 0
	for _, f := range s.Files {
		if !f.Dir {
			n++
		}
	}
	return n
}

// Note names one path and why it was not copied, replaced or removed.
type Note struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

// Report is the outcome of one deploy's file copy, shown with the deploy
// result.
type Report struct {
	// Copied counts the files that now hold the server's content.
	Copied int `json:"copied"`
	// Removed counts files gone from the server since the last deploy.
	Removed int `json:"removed"`
	// Kept lists files on the agent that were left as they are.
	Kept []Note `json:"kept,omitempty"`
	// Skipped lists entries the server left out of the set.
	Skipped []Note `json:"skipped,omitempty"`
	// NotSent counts the files an agent too old to receive them did not
	// get.
	NotSent int `json:"not_sent,omitempty"`
}

// managedTop matches the top-level files a deploy writes itself or that
// only the server uses: compose files and environment overrides.
var managedTop = regexp.MustCompile(`^(docker-)?compose(\.[^/]+)?\.ya?ml$`)

// Skip reports whether rel (slash-separated, relative to the stack
// directory) stays out of a set: the compose files and overrides, .env and
// its encrypted form (secrets never go to agents as files), git metadata
// and Dockmesh's own files.
func Skip(rel string) bool {
	rel = strings.ToLower(rel)
	top, _, nested := strings.Cut(rel, "/")
	if strings.HasPrefix(top, ".git") || strings.HasPrefix(top, ".dockmesh") {
		return true
	}
	if strings.HasPrefix(path.Base(rel), tempPrefix) {
		return true
	}
	if nested {
		return false
	}
	return rel == ".env" || rel == ".env.age" || managedTop.MatchString(rel)
}

// checkName refuses a name the JSON frame would not carry unchanged or
// that is too long.
func checkName(p string) error {
	switch {
	case p == "" || len(p) > MaxPathBytes:
		return fmt.Errorf("path empty or longer than %d bytes", MaxPathBytes)
	case !utf8.ValidString(p):
		return errors.New("path is not valid UTF-8")
	case strings.ContainsFunc(p, func(r rune) bool { return r == '\\' || unicode.IsControl(r) }):
		return errors.New("path contains a backslash or control character")
	}
	return nil
}

// checkPath refuses a set entry the agent must not write.
func checkPath(p string) error {
	if err := checkName(p); err != nil {
		return fmt.Errorf("stack file path %q: %w", p, err)
	}
	switch {
	case p == "." || !fs.ValidPath(p):
		return fmt.Errorf("stack file path %q: not inside the stack directory", p)
	case Skip(p):
		return fmt.Errorf("stack file path %q: managed by the deploy itself", p)
	}
	return nil
}

// Validate checks a set received from the server against the same limits
// Collect applies.
func (s *Set) Validate() error {
	if len(s.Files) > MaxEntries {
		return fmt.Errorf("stack files: %d entries, more than %d", len(s.Files), MaxEntries)
	}
	if s.Size() > MaxTotalBytes {
		return fmt.Errorf("stack files: %d bytes, more than %d", s.Size(), MaxTotalBytes)
	}
	var encoded int64
	for _, f := range s.Files {
		encoded += encodedSize(f.Path, int64(len(f.Data)))
	}
	if encoded > MaxEncodedBytes {
		return fmt.Errorf("stack files: about %d bytes encoded, more than %d", encoded, MaxEncodedBytes)
	}
	seen := make(map[string]bool, len(s.Files))
	for _, f := range s.Files {
		if err := checkPath(f.Path); err != nil {
			return err
		}
		if seen[f.Path] {
			return fmt.Errorf("stack file path %q: listed twice", f.Path)
		}
		seen[f.Path] = true
		if f.Dir && len(f.Data) > 0 {
			return fmt.Errorf("stack file path %q: a directory with data", f.Path)
		}
	}
	return nil
}

func mib(n int64) string { return fmt.Sprintf("%.1f MiB", float64(n)/(1<<20)) }

// Manifest records what Apply wrote into one stack directory: the sha256
// and size of each file's content and the directories it created. It lives outside
// the stack directory, where the stack's containers cannot change it.
type Manifest struct {
	Files map[string]string `json:"files"`
	Sizes map[string]int64  `json:"sizes,omitempty"`
	Dirs  []string          `json:"dirs,omitempty"`
}

const tempPrefix = ".dockmesh-tmp-"

// TooLargeError refuses a stack directory over the limits of one set.
type TooLargeError struct {
	Entries int
	Bytes   int64
	Largest []Note
}

func (e *TooLargeError) Error() string {
	var b strings.Builder
	if e.Entries > MaxEntries {
		fmt.Fprintf(&b, "the stack directory holds more than %d files and directories", MaxEntries)
	} else {
		fmt.Fprintf(&b, "the files next to compose.yaml add up to %s", mib(e.Bytes))
	}
	fmt.Fprintf(&b, ", more than a deploy carries to an agent (%d entries, %s of data, %s with the names)", MaxEntries, mib(MaxTotalBytes), mib(MaxEncodedBytes))
	if len(e.Largest) > 0 {
		parts := make([]string, len(e.Largest))
		for i, n := range e.Largest {
			parts[i] = n.Path + " (" + n.Reason + ")"
		}
		b.WriteString("; largest: " + strings.Join(parts, ", "))
	}
	b.WriteString(". Keep data in a volume and large files in the image")
	return b.String()
}
