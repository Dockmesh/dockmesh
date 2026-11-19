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

// WithConfirmation carries c to code that checks the stack files itself
// later, such as the image updater. Confirmations of other stacks ctx
// already carries stay.
func WithConfirmation(ctx context.Context, c *Confirmation) context.Context {
	prev, _ := ctx.Value(confirmationKey{}).(map[string]*Confirmation)
	next := make(map[string]*Confirmation, len(prev)+1)
	maps.Copy(next, prev)
	next[c.Snap.Name] = c
	return context.WithValue(ctx, confirmationKey{}, next)
}

// ConfirmationFrom returns the confirmation for stack name that ctx
// carries, nil without one.
func ConfirmationFrom(ctx context.Context, name string) *Confirmation {
	all, _ := ctx.Value(confirmationKey{}).(map[string]*Confirmation)
	return all[name]
}

// Confirms reports whether c covers the outside changes snap found: the
// same files with the same content as the snapshot the user confirmed.
func (c *Confirmation) Confirms(snap *Snapshot) bool {
	return c != nil && c.Snap.Name == snap.Name && maps.Equal(c.Snap.sums, snap.sums)
}

// RequireConfirmed refuses the outside changes of snap unless ctx carries a
// confirmation of exactly these files. It returns the confirmation to
// accept before the first write, nil when nothing changed.
func RequireConfirmed(ctx context.Context, snap *Snapshot) (*Confirmation, error) {
	if len(snap.Changed) == 0 {
		return nil, nil
	}
	c := ConfirmationFrom(ctx, snap.Name)
	if !c.Confirms(snap) {
		return nil, fmt.Errorf("%w: %s", ErrModifiedOutside, strings.Join(snap.Changed, ", "))
	}
	return c, nil
}
