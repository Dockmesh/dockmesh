// Command checkrelease refuses binaries that must not be published: built
// with the labkeys tag, trusting an update-signing key other than the
// release and rotation keys, or carrying a DOCKMESH_LAB_ hook. The release
// workflow runs it on every binary it built, before anything is packaged or
// signed.
//
//	checkrelease dist/dockmesh dist/dockmesh-agent dist/dmctl
package main

import (
	"bytes"
	"debug/buildinfo"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime/debug"
	"strings"

	"aead.dev/minisign"

	"github.com/dockmesh/dockmesh/internal/releasesig"
)

// keyedCommands maps the main package of every released command to whether
// it embeds the update-signing keys. A command missing here is refused, so
// a new binary in the release gets classified before it ships.
var keyedCommands = map[string]bool{
	"github.com/dockmesh/dockmesh/cmd/dockmesh":       true,
	"github.com/dockmesh/dockmesh/cmd/dockmesh-agent": true,
	"github.com/dockmesh/dockmesh/cmd/dmctl":          false,
}

var releaseKeys = []string{releasesig.ReleaseKey, releasesig.RotationKey}

const labHookPrefix = "DOCKMESH_LAB_"

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "checkrelease:", err)
		os.Exit(1)
	}
}

func run(paths []string, stdout io.Writer) error {
	if len(paths) == 0 {
		return errors.New("usage: checkrelease binary [binary ...]")
	}
	failed := 0
	for _, p := range paths {
		problems, err := checkFile(p)
		if err != nil {
			return err
		}
		if len(problems) == 0 {
			fmt.Fprintf(stdout, "ok   %s\n", p)
			continue
		}
		failed++
		for _, pr := range problems {
			fmt.Fprintf(stdout, "FAIL %s: %s\n", p, pr)
		}
	}
	if failed > 0 {
		return fmt.Errorf("%d of %d binaries must not be released", failed, len(paths))
	}
	return nil
}

func checkFile(path string) ([]string, error) {
	data, err := os.ReadFile(path) //nolint:gosec // path from the CLI caller
	if err != nil {
		return nil, err
	}
	info, err := buildinfo.Read(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("%s: read build info: %w", path, err)
	}
	return check(info, data), nil
}

func check(info *debug.BuildInfo, data []byte) []string {
	var problems []string
	keyed, known := keyedCommands[info.Path]
	if !known {
		problems = append(problems, fmt.Sprintf("unknown command %q, add it to keyedCommands", info.Path))
	}
	for _, s := range info.Settings {
		if s.Key == "-tags" && strings.Contains(s.Value, "labkeys") {
			problems = append(problems, "built with -tags="+s.Value)
		}
	}
	if bytes.Contains(data, []byte(labHookPrefix)) {
		problems = append(problems, "contains "+labHookPrefix+" hooks")
	}
	found := map[string]bool{}
	for _, k := range minisignKeys(data) {
		found[k] = true
		if !isReleaseKey(k) {
			problems = append(problems, fmt.Sprintf("trusts unknown minisign key %s", describeKey(k)))
		}
	}
	if keyed {
		for _, k := range releaseKeys {
			if !found[k] {
				problems = append(problems, fmt.Sprintf("misses release key %s", describeKey(k)))
			}
		}
	}
	return problems
}

func isReleaseKey(k string) bool {
	for _, r := range releaseKeys {
		if k == r {
			return true
		}
	}
	return false
}

func describeKey(k string) string {
	var pk minisign.PublicKey
	if err := pk.UnmarshalText([]byte(k)); err != nil {
		return k
	}
	return fmt.Sprintf("%016X (%s)", pk.ID(), k)
}

// minisignKeyLen is the base64 length of a minisign public key: "Ed", the
// 8-byte key ID and the 32-byte Ed25519 key, which always encodes to a
// string starting with "RW".
const minisignKeyLen = 56

// minisignKeys returns every distinct minisign public key in data. Go packs
// string constants back to back, so a key is usually framed by other
// letters; every "RW" is tried as a start instead of looking for word
// boundaries.
func minisignKeys(data []byte) []string {
	var keys []string
	seen := map[string]bool{}
	for i := 0; ; i++ {
		j := bytes.Index(data[i:], []byte("RW"))
		if j < 0 {
			return keys
		}
		i += j
		if i+minisignKeyLen > len(data) {
			return keys
		}
		cand := data[i : i+minisignKeyLen]
		if !isBase64(cand) {
			continue
		}
		var pk minisign.PublicKey
		if pk.UnmarshalText(cand) != nil {
			continue
		}
		if k := string(cand); !seen[k] {
			seen[k] = true
			keys = append(keys, k)
		}
	}
}

func isBase64(b []byte) bool {
	for _, c := range b {
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '+', c == '/':
		default:
			return false
		}
	}
	return true
}
