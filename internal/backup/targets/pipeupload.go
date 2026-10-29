package targets

import (
	"errors"
	"io"
)

var errUploadAborted = errors.New("upload aborted")

// pipeUpload feeds writes into an upload that reads from a pipe in its own
// goroutine, so archives stream to the target instead of being buffered.
type pipeUpload struct {
	pw      *io.PipeWriter
	done    chan error
	discard func()
}

// startPipeUpload runs upload in the background. discard removes whatever
// the server may have kept of an upload that was aborted or failed.
func startPipeUpload(upload func(io.Reader) error, discard func()) *pipeUpload {
	pr, pw := io.Pipe()
	u := &pipeUpload{pw: pw, done: make(chan error, 1), discard: discard}
	go func() {
		err := upload(pr)
		if err == nil {
			err = errUploadAborted
			if _, rerr := pr.Read(make([]byte, 1)); errors.Is(rerr, io.EOF) {
				err = nil
			}
		}
		// Unblocks a writer if the upload stopped reading early.
		pr.CloseWithError(err)
		u.done <- err
	}()
	return u
}

func (u *pipeUpload) Write(p []byte) (int, error) { return u.pw.Write(p) }

func (u *pipeUpload) Close() error {
	_ = u.pw.Close()
	err := <-u.done
	if err != nil && u.discard != nil {
		u.discard()
	}
	return err
}

func (u *pipeUpload) Abort() error {
	_ = u.pw.CloseWithError(errUploadAborted)
	<-u.done
	if u.discard != nil {
		u.discard()
	}
	return nil
}
