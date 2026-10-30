package targets

import (
	"bytes"
	"errors"
	"io"
	"testing"
)

var errBadEOF = errors.New("invalid response error: unverified packet returned")

// eofFailingFile serves data the way go-smb2 does against ksmbd: reads
// inside the file succeed, a read at or past the end fails with a
// non-EOF error.
type eofFailingFile struct {
	data     []byte
	off      int
	pastEnd  int
	maxChunk int
}

func (f *eofFailingFile) Read(p []byte) (int, error) {
	if f.off >= len(f.data) {
		f.pastEnd++
		return 0, errBadEOF
	}
	if f.maxChunk > 0 && len(p) > f.maxChunk {
		p = p[:f.maxChunk]
	}
	n := copy(p, f.data[f.off:])
	f.off += n
	return n, nil
}

func TestSizedReaderStopsAtFileSize(t *testing.T) {
	payload := bytes.Repeat([]byte("dockmesh"), 11000)
	tests := []struct {
		name     string
		size     int
		maxChunk int
	}{
		{"empty file", 0, 0},
		{"one byte", 1, 0},
		{"small file", 1000, 0},
		{"one age chunk", 64 * 1024, 0},
		{"server returns short chunks", 87000, 4096},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &eofFailingFile{data: payload[:tt.size], maxChunk: tt.maxChunk}
			r := &sizedReader{r: f, remaining: int64(tt.size)}
			got, err := io.ReadAll(r)
			if err != nil {
				t.Fatalf("read: %v", err)
			}
			if !bytes.Equal(got, payload[:tt.size]) {
				t.Fatalf("got %d bytes, want %d", len(got), tt.size)
			}
			if f.pastEnd != 0 {
				t.Fatalf("%d reads past the end of the file", f.pastEnd)
			}
		})
	}
}

func TestSizedReaderReportsTruncation(t *testing.T) {
	r := &sizedReader{r: bytes.NewReader([]byte("short")), remaining: 10}
	got, err := io.ReadAll(r)
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("err = %v, want io.ErrUnexpectedEOF", err)
	}
	if string(got) != "short" {
		t.Fatalf("got %q", got)
	}
}
