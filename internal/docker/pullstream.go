package docker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/client"
	"github.com/dockmesh/dockmesh/internal/operations"
)

// ReadPullStream reads an image pull stream to the end, as the pull only
// finishes once it is consumed. Docker reports a failed pull (rate limit,
// auth, missing tag) inside the stream with an HTTP 200, so the first
// error line becomes the returned error, with secrets masked.
func ReadPullStream(r io.Reader, secrets []string) error {
	var pullErr error
	dec := json.NewDecoder(r)
	for {
		var m struct {
			Error string `json:"error"`
		}
		if err := dec.Decode(&m); err != nil {
			if errors.Is(err, io.EOF) {
				return pullErr
			}
			if errors.Is(err, io.ErrUnexpectedEOF) {
				if pullErr != nil {
					return pullErr
				}
				return fmt.Errorf("pull stream cut off: %w", err)
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

// PullAnonymous pulls ref without credentials and waits until it is done,
// for the images Dockmesh runs itself.
func PullAnonymous(ctx context.Context, cli client.ImageAPIClient, ref string) error {
	rc, err := cli.ImagePull(ctx, ref, image.PullOptions{})
	if err != nil {
		return err
	}
	defer rc.Close()
	return ReadPullStream(rc, nil)
}
