// Package releasesig verifies Dockmesh release downloads: the minisign
// signature over checksums.txt, the tag named in its trusted comment, and
// the SHA-256 of one artifact listed in that file.
package releasesig

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"

	"aead.dev/minisign"
)

// ChecksumsFile is the release asset that lists the SHA-256 of every other
// asset; SignatureFile is its minisign signature.
const (
	ChecksumsFile = "checksums.txt"
	SignatureFile = ChecksumsFile + ".minisig"
)

// Release signing keys. ReleaseKey signs every release; RotationKey is held
// offline and only signs the release that ships a new key pair, so a leaked
// CI secret can be replaced without trusting anything the leak could forge.
const (
	ReleaseKey  = "RWRaHI41HpSgAcJDtFP8X2HnLIjHDLUdnj0GyCoGiGCdDW1rlLDWXzK1"
	RotationKey = "RWQDX3H4Uce7c0kKscCE0WRymm/CkSETtzGn4IKKN3tdJyfO9y6ZyuLA"
)

var tagPattern = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$`)

var (
	ErrSignature = errors.New("release signature does not verify")
	ErrTag       = errors.New("release signature is for a different release")
	ErrChecksum  = errors.New("artifact checksum does not match checksums.txt")
	ErrNotListed = errors.New("artifact is not listed in checksums.txt")
	ErrDuplicate = errors.New("artifact is listed more than once in checksums.txt")
)

// ReleaseKeyID returns the hex key ID of ReleaseKey as minisign prints it.
func ReleaseKeyID() string {
	return fmt.Sprintf("%016X", Default().keys[0].ID())
}

// TrustedComment is the signed comment every release signature carries. It
// names the tag, so a signature from one release can't vouch for the files
// of another.
func TrustedComment(tag string) string {
	return "dockmesh " + tag + " " + ChecksumsFile
}

// Verifier checks signatures against a fixed key set.
type Verifier struct {
	keys []minisign.PublicKey
}

// Default returns a Verifier holding the embedded release and rotation keys.
func Default() *Verifier {
	v, err := New(ReleaseKey, RotationKey)
	if err != nil {
		panic(err)
	}
	return v
}

// New returns a Verifier for the given base64 minisign public keys.
func New(keys ...string) (*Verifier, error) {
	if len(keys) == 0 {
		return nil, errors.New("releasesig: no public keys")
	}
	v := &Verifier{}
	for _, k := range keys {
		var pk minisign.PublicKey
		if err := pk.UnmarshalText([]byte(k)); err != nil {
			return nil, fmt.Errorf("releasesig: public key %q: %w", k, err)
		}
		v.keys = append(v.keys, pk)
	}
	return v, nil
}

// VerifyChecksums checks that sig is a valid signature of checksums by one of
// the verifier's keys and that its trusted comment names tag. It returns the
// ID of the key that signed. An older release verifies as well as a newer
// one, so callers refuse tags that are not newer than what is installed.
func (v *Verifier) VerifyChecksums(checksums, sig []byte, tag string) (uint64, error) {
	if !tagPattern.MatchString(tag) {
		return 0, fmt.Errorf("%w: %q is not a release tag", ErrTag, tag)
	}
	var s minisign.Signature
	if err := s.UnmarshalText(sig); err != nil {
		return 0, fmt.Errorf("%w: %w", ErrSignature, err)
	}
	for _, k := range v.keys {
		if k.ID() != s.KeyID {
			continue
		}
		if !minisign.Verify(k, checksums, sig) {
			return 0, ErrSignature
		}
		if s.TrustedComment != TrustedComment(tag) {
			return 0, fmt.Errorf("%w: signed %q, want %q", ErrTag, s.TrustedComment, TrustedComment(tag))
		}
		return k.ID(), nil
	}
	return 0, fmt.Errorf("%w: signed by unknown key %016X", ErrSignature, s.KeyID)
}

// VerifyArtifact verifies the signature of checksums for tag, then checks
// that artifact hashes to the entry for name. The artifact is read to EOF.
func (v *Verifier) VerifyArtifact(checksums, sig []byte, tag, name string, artifact io.Reader) error {
	if _, err := v.VerifyChecksums(checksums, sig, tag); err != nil {
		return err
	}
	want, err := Checksum(checksums, name)
	if err != nil {
		return err
	}
	h := sha256.New()
	if _, err := io.Copy(h, artifact); err != nil {
		return fmt.Errorf("releasesig: read %s: %w", name, err)
	}
	if !bytes.Equal(h.Sum(nil), want) {
		return fmt.Errorf("%w: %s", ErrChecksum, name)
	}
	return nil
}

// Checksum returns the SHA-256 listed for name in a sha256sum-style file.
// A name listed twice is refused rather than resolved either way.
func Checksum(checksums []byte, name string) ([]byte, error) {
	var found []byte
	sc := bufio.NewScanner(bytes.NewReader(checksums))
	for sc.Scan() {
		line := sc.Text()
		sum, file, ok := strings.Cut(line, "  ")
		if !ok {
			sum, file, ok = strings.Cut(line, " *")
		}
		if !ok || file != name {
			continue
		}
		b, err := hex.DecodeString(sum)
		if err != nil || len(b) != sha256.Size {
			return nil, fmt.Errorf("releasesig: malformed checksum for %s", name)
		}
		if found != nil {
			return nil, fmt.Errorf("%w: %s", ErrDuplicate, name)
		}
		found = b
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("releasesig: read %s: %w", ChecksumsFile, err)
	}
	if found == nil {
		return nil, fmt.Errorf("%w: %s", ErrNotListed, name)
	}
	return found, nil
}
