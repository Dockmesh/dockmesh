package stacks

import (
	"context"
	"fmt"
	"maps"
	"strings"
)

// Confirmation is a request's confirmation of the stack files changed
// outside Dockmesh. It holds the snapshot the user was shown; Accept records
// those files as reviewed and is meant to run after the last refusal,
// right before the first write, so a refused request leaves them unreviewed.
type Confirmation struct {
	Snap   *Snapshot
	Accept func(ctx context.Context) error
}

type confirmationKey struct{}

// WithConfirmation carries c to code that checks or writes the stack
// itself later, such as the image updater or the compose engine.
// Confirmations of other stacks ctx already carries stay.
func WithConfirmation(ctx context.Context, c *Confirmation) context.Context {
	prev, _ := ctx.Value(confirmationKey{}).(map[string]*Confirmation)
	next := make(map[string]*Confirmation, len(prev)+1)
	maps.Copy(next, prev)
	next[c.Snap.Name] = c
	return context.WithValue(ctx, confirmationKey{}, next)
}

func confirmationFrom(ctx context.Context, name string) *Confirmation {
	all, _ := ctx.Value(confirmationKey{}).(map[string]*Confirmation)
	return all[name]
}

// RequireConfirmed refuses the outside changes of snap unless ctx carries a
// confirmation of exactly these files: the same files with the same
// content. It returns the confirmation to accept before the first write,
// nil when nothing changed.
func RequireConfirmed(ctx context.Context, snap *Snapshot) (*Confirmation, error) {
	if len(snap.Changed) == 0 {
		return nil, nil
	}
	c := confirmationFrom(ctx, snap.Name)
	if c == nil || !maps.Equal(c.Snap.sums, snap.sums) {
		return nil, fmt.Errorf("%w: %s", ErrModifiedOutside, strings.Join(snap.Changed, ", "))
	}
	return c, nil
}

// AcceptConfirmed records the confirmation ctx carries for stack name, if
// any. Hosts call it right before their first write.
func AcceptConfirmed(ctx context.Context, name string) error {
	if c := confirmationFrom(ctx, name); c != nil {
		return c.Accept(ctx)
	}
	return nil
}
