package updater

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/dockmesh/dockmesh/internal/operations"
)

// pullMessage is one line of the JSON stream the Docker daemon sends
// while pulling.
type pullMessage struct {
	Status   string `json:"status"`
	ID       string `json:"id"`
	Progress struct {
		Current int64 `json:"current"`
		Total   int64 `json:"total"`
	} `json:"progressDetail"`
	Error string `json:"error"`
}

const progressEvery = 250 * time.Millisecond

// drainPull reads the pull stream to the end, as the pull only finishes
// once it is consumed, and reports layer progress and status lines to op.
// A stream that stops parsing is still read to the end.
func drainPull(rc io.Reader, op *operations.Op) error {
	const step = StepPull
	if op == nil {
		_, err := io.Copy(io.Discard, rc)
		return err
	}
	type layer struct{ cur, total int64 }
	layers := map[string]*layer{}
	var order []string
	var last time.Time
	dec := json.NewDecoder(rc)
	for {
		var m pullMessage
		if err := dec.Decode(&m); err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				return nil
			}
			var se *json.SyntaxError
			var te *json.UnmarshalTypeError
			if !errors.As(err, &se) && !errors.As(err, &te) {
				return err
			}
			_, cerr := io.Copy(io.Discard, io.MultiReader(dec.Buffered(), rc))
			return cerr
		}
		switch {
		case m.Error != "":
			op.Logf("error", "%s", m.Error)
		case m.ID == "":
			op.Logf("info", "%s", m.Status)
		case m.Status == "Downloading":
			l := layers[m.ID]
			if l == nil {
				l = &layer{}
				layers[m.ID] = l
				order = append(order, m.ID)
			}
			l.cur, l.total = m.Progress.Current, m.Progress.Total
		case m.Status == "Download complete", m.Status == "Pull complete", m.Status == "Already exists":
			if l := layers[m.ID]; l != nil {
				l.cur = l.total
			}
			if m.Status != "Download complete" {
				op.Logf("info", "%s: %s", m.ID, m.Status)
			}
		}
		if len(order) > 0 && time.Since(last) >= progressEvery {
			var cur, total int64
			for _, id := range order {
				cur += layers[id].cur
				total += layers[id].total
			}
			if total > 0 {
				op.StepProgress(step, float64(cur)/float64(total), fmt.Sprintf("%s of %s", humanBytes(cur), humanBytes(total)))
				last = time.Now()
			}
		}
	}
}

func humanBytes(n int64) string {
	const unit = 1000
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "kMGTPE"[exp])
}

// authSecrets returns the credential values inside a base64 registry
// auth blob, so they are masked from the operation's output.
func authSecrets(blob string) []string {
	if blob == "" {
		return nil
	}
	raw, err := base64.URLEncoding.DecodeString(blob)
	if err != nil {
		if raw, err = base64.StdEncoding.DecodeString(blob); err != nil {
			return []string{blob}
		}
	}
	var a struct {
		Password      string `json:"password"`
		Auth          string `json:"auth"`
		IdentityToken string `json:"identitytoken"`
		RegistryToken string `json:"registrytoken"`
	}
	_ = json.Unmarshal(raw, &a)
	return []string{blob, a.Password, a.Auth, a.IdentityToken, a.RegistryToken}
}
