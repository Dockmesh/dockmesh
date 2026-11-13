// Command relsign signs and verifies the checksums.txt of a release with
// minisign. The release workflow signs with the key from the environment and
// then verifies the result against the keys embedded in Dockmesh.
//
//	relsign sign -tag v0.6.0 -in checksums.txt -out checksums.txt.minisig
//	relsign verify -tag v0.6.0 -in checksums.txt -sig checksums.txt.minisig [-release-key | -key-id 01A0941E358E1C5A] [-pubkey <base64>]...
//	relsign keygen -pub test.pub -secret test.key
//
// sign reads the encrypted secret key from MINISIGN_SECRET_KEY and its
// password from MINISIGN_PASSWORD; keygen encrypts a throwaway key with
// MINISIGN_PASSWORD, for dry runs only.
package main

import (
	"crypto/rand"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"

	"aead.dev/minisign"

	"github.com/dockmesh/dockmesh/internal/releasesig"
)

var tagPattern = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$`)

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Getenv); err != nil {
		fmt.Fprintln(os.Stderr, "relsign:", err)
		os.Exit(1)
	}
}

func run(args []string, stdout io.Writer, getenv func(string) string) error {
	if len(args) == 0 {
		return errors.New("usage: relsign sign|verify|keygen [flags]")
	}
	switch args[0] {
	case "sign":
		return runSign(args[1:], stdout, getenv)
	case "verify":
		return runVerify(args[1:], stdout)
	case "keygen":
		return runKeygen(args[1:], stdout, getenv)
	default:
		return fmt.Errorf("unknown command %q (want sign, verify or keygen)", args[0])
	}
}

type multiFlag []string

func (m *multiFlag) String() string     { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error { *m = append(*m, v); return nil }

func checkTag(tag string) error {
	if !tagPattern.MatchString(tag) {
		return fmt.Errorf("-tag %q is not a release tag like v1.2.3 or v1.2.3-rc.1", tag)
	}
	return nil
}

func runSign(args []string, stdout io.Writer, getenv func(string) string) error {
	fl := flag.NewFlagSet("sign", flag.ContinueOnError)
	tag := fl.String("tag", "", "release tag the signature is for")
	in := fl.String("in", releasesig.ChecksumsFile, "file to sign")
	out := fl.String("out", releasesig.SignatureFile, "signature output")
	if err := fl.Parse(args); err != nil {
		return err
	}
	if err := checkTag(*tag); err != nil {
		return err
	}
	secret, password := getenv("MINISIGN_SECRET_KEY"), getenv("MINISIGN_PASSWORD")
	if secret == "" || password == "" {
		return errors.New("MINISIGN_SECRET_KEY and MINISIGN_PASSWORD must be set")
	}
	priv, err := minisign.DecryptKey(password, []byte(strings.TrimSpace(secret)))
	if err != nil {
		return fmt.Errorf("decrypt secret key: %w", err)
	}
	msg, err := os.ReadFile(*in)
	if err != nil {
		return err
	}
	if err := os.WriteFile(*out, sign(priv, msg, *tag), 0o600); err != nil { //nolint:gosec // path is the operator's own -out flag
		return err
	}
	fmt.Fprintf(stdout, "signed %s for %s with key %016X\n", *in, *tag, priv.ID())
	return nil
}

func sign(priv minisign.PrivateKey, msg []byte, tag string) []byte {
	untrusted := fmt.Sprintf("signature from dockmesh release key %016X", priv.ID())
	return minisign.SignWithComments(priv, msg, releasesig.TrustedComment(tag), untrusted)
}

func runVerify(args []string, stdout io.Writer) error {
	fl := flag.NewFlagSet("verify", flag.ContinueOnError)
	tag := fl.String("tag", "", "release tag the signature must name")
	in := fl.String("in", releasesig.ChecksumsFile, "signed file")
	sigPath := fl.String("sig", releasesig.SignatureFile, "signature file")
	keyID := fl.String("key-id", "", "require this signing key (hex key id)")
	releaseKey := fl.Bool("release-key", false, "require the embedded release key, not the rotation key")
	var pubkeys multiFlag
	fl.Var(&pubkeys, "pubkey", "trust this base64 public key instead of the embedded ones (repeatable)")
	if err := fl.Parse(args); err != nil {
		return err
	}
	if err := checkTag(*tag); err != nil {
		return err
	}
	if *releaseKey {
		if *keyID != "" {
			return errors.New("-release-key and -key-id exclude each other")
		}
		*keyID = releasesig.ReleaseKeyID()
	}
	v := releasesig.Default()
	if len(pubkeys) > 0 {
		var err error
		if v, err = releasesig.New(pubkeys...); err != nil {
			return err
		}
	}
	msg, err := os.ReadFile(*in)
	if err != nil {
		return err
	}
	sig, err := os.ReadFile(*sigPath)
	if err != nil {
		return err
	}
	id, err := v.VerifyChecksums(msg, sig, *tag)
	if err != nil {
		return err
	}
	if *keyID != "" {
		want, err := strconv.ParseUint(*keyID, 16, 64)
		if err != nil {
			return fmt.Errorf("-key-id %q: %w", *keyID, err)
		}
		if id != want {
			return fmt.Errorf("signed by key %016X, want %016X", id, want)
		}
	}
	fmt.Fprintf(stdout, "verified %s for %s, key %016X\n", *in, *tag, id)
	return nil
}

func runKeygen(args []string, stdout io.Writer, getenv func(string) string) error {
	fl := flag.NewFlagSet("keygen", flag.ContinueOnError)
	pubOut := fl.String("pub", "", "public key output")
	secretOut := fl.String("secret", "", "encrypted secret key output")
	if err := fl.Parse(args); err != nil {
		return err
	}
	if *pubOut == "" || *secretOut == "" {
		return errors.New("-pub and -secret are required")
	}
	password := getenv("MINISIGN_PASSWORD")
	if password == "" {
		return errors.New("MINISIGN_PASSWORD must be set")
	}
	pub, priv, err := minisign.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	enc, err := minisign.EncryptKey(password, priv)
	if err != nil {
		return err
	}
	if err := os.WriteFile(*secretOut, enc, 0o600); err != nil {
		return err
	}
	if err := os.WriteFile(*pubOut, []byte(pub.String()+"\n"), 0o600); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "throwaway key %016X\n", pub.ID())
	return nil
}
