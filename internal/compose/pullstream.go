package compose

import (
	"encoding/json"
	"errors"
	"io"

	"github.com/dockmesh/dockmesh/internal/operations"
)

// readPullStream reads an image pull stream to the end, as the pull only
// finishes once it is consumed. Docker reports a failed pull (rate limit,
// auth, missing tag) inside the stream with an HTTP 200, so the first
// error line becomes the returned error, with secrets masked.
func readPullStream(r io.Reader, secrets []string) error {
	var pullErr error
	dec := json.NewDecoder(r)
	for {
		var m struct {
			Error string `json:"error"`
		}
		if err := dec.Decode(&m); err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				return pullErr
			}
			var se *json.SyntaxError
			var te *json.UnmarshalTypeError
			if !errors.As(err, &se) && !errors.As(err, &te) {
				return err
			}
			if _, cerr := io.Copy(io.Discard, io.MultiReader(dec.Buffered(), r)); cerr != nil {
				return cerr
			}
			return pullErr
		}
		if m.Error != "" && pullErr == nil {
			pullErr = errors.New(operations.MaskText(m.Error, secrets))
		}
	}
}
