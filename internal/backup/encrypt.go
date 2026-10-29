package backup

import (
	"io"

	"github.com/dockmesh/dockmesh/internal/backup/targets"
	"github.com/dockmesh/dockmesh/internal/secrets"
	"filippo.io/age"
)

// encryptingWriter wraps a target writer with age streaming encryption.
// Closing flushes the age stream and then closes the underlying target,
// so the upload only finalises after the ciphertext is fully written.
type encryptingWriter struct {
	enc       io.WriteCloser
	target    io.WriteCloser
}

func (e *encryptingWriter) Write(p []byte) (int, error) { return e.enc.Write(p) }

func (e *encryptingWriter) Close() error {
	if err := e.enc.Close(); err != nil {
		_ = abortWriter(e.target)
		return err
	}
	return e.target.Close()
}

// Abort drops the upload without flushing the age stream.
func (e *encryptingWriter) Abort() error { return abortWriter(e.target) }

// abortWriter drops an unfinished upload where the target supports it and
// otherwise just closes the file; the caller deletes what was written.
func abortWriter(w io.WriteCloser) error {
	if a, ok := w.(targets.Aborter); ok {
		return a.Abort()
	}
	return w.Close()
}

// wrapEncrypt produces a writer that encrypts to the underlying target
// using the secrets service's recipient. If the secrets service is
// disabled the target is returned unchanged.
func wrapEncrypt(target io.WriteCloser, sec *secrets.Service) (io.WriteCloser, error) {
	if sec == nil || !sec.Enabled() {
		return target, nil
	}
	rec, err := age.ParseX25519Recipient(sec.PublicRecipient())
	if err != nil {
		return nil, err
	}
	enc, err := age.Encrypt(target, rec)
	if err != nil {
		return nil, err
	}
	return &encryptingWriter{enc: enc, target: target}, nil
}

// wrapDecrypt is the restore-side counterpart. It returns a reader that
// decrypts the source on-the-fly.
func wrapDecrypt(src io.ReadCloser, sec *secrets.Service) (io.ReadCloser, error) {
	if sec == nil || !sec.Enabled() {
		return src, nil
	}
	ids, err := sec.Identities()
	if err != nil {
		return nil, err
	}
	r, err := age.Decrypt(src, ids...)
	if err != nil {
		return nil, err
	}
	return &decryptingReader{r: r, src: src}, nil
}

type decryptingReader struct {
	r   io.Reader
	src io.ReadCloser
}

func (d *decryptingReader) Read(p []byte) (int, error) { return d.r.Read(p) }
func (d *decryptingReader) Close() error               { return d.src.Close() }
