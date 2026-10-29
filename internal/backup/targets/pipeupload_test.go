package targets

import (
	"bytes"
	"errors"
	"io"
	"testing"
)

func TestPipeUpload(t *testing.T) {
	t.Run("streams everything", func(t *testing.T) {
		var got bytes.Buffer
		u := startPipeUpload(func(r io.Reader) error {
			_, err := io.Copy(&got, r)
			return err
		}, func() { t.Error("discard on success") })
		for i := 0; i < 100; i++ {
			if _, err := u.Write(bytes.Repeat([]byte{byte(i)}, 1000)); err != nil {
				t.Fatal(err)
			}
		}
		if err := u.Close(); err != nil {
			t.Fatal(err)
		}
		if got.Len() != 100_000 {
			t.Fatalf("uploaded %d bytes", got.Len())
		}
	})

	t.Run("upload error reaches the writer", func(t *testing.T) {
		discarded := false
		boom := errors.New("403 forbidden")
		u := startPipeUpload(func(io.Reader) error { return boom }, func() { discarded = true })
		var err error
		for i := 0; i < 10 && err == nil; i++ {
			_, err = u.Write([]byte("x"))
		}
		if !errors.Is(err, boom) {
			t.Fatalf("write error %v", err)
		}
		if err := u.Close(); !errors.Is(err, boom) {
			t.Fatalf("close error %v", err)
		}
		if !discarded {
			t.Fatal("failed upload not discarded")
		}
	})

	t.Run("abort fails the upload", func(t *testing.T) {
		var seen error
		discarded := false
		u := startPipeUpload(func(r io.Reader) error {
			_, seen = io.Copy(io.Discard, r)
			return seen
		}, func() { discarded = true })
		_, _ = u.Write([]byte("partial"))
		if err := u.Abort(); err != nil {
			t.Fatal(err)
		}
		if !errors.Is(seen, errUploadAborted) || !discarded {
			t.Fatalf("upload saw %v, discarded=%v", seen, discarded)
		}
	})
}

func TestPipeUploadClosedBody(t *testing.T) {
	// net/http closes the request body after sending it.
	u := startPipeUpload(func(r io.Reader) error {
		if _, err := io.Copy(io.Discard, r); err != nil {
			return err
		}
		if c, ok := r.(io.Closer); ok {
			_ = c.Close()
		}
		return nil
	}, nil)
	_, _ = u.Write([]byte("archive"))
	if err := u.Close(); err != nil {
		t.Fatalf("complete upload reported as %v", err)
	}
}
