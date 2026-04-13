package backup

import (
	"io"

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
		_ = e.target.Close()
		return err
	}
	return e.target.Close()
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
	id, err := sec.Identity()
	if err != nil {
		return nil, err
	}
	r, err := age.Decrypt(src, id)
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
