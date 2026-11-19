package stacks

import (
	"context"
	"testing"
)

func TestAcceptConfirmedRunsOnlyTheNamedStack(t *testing.T) {
	var accepted []string
	confirm := func(name string) *Confirmation {
		return &Confirmation{Snap: &Snapshot{Name: name}, Accept: func(context.Context) error {
			accepted = append(accepted, name)
			return nil
		}}
	}
	ctx := WithConfirmation(WithConfirmation(context.Background(), confirm("app")), confirm("db"))
	for _, name := range []string{"db", "other"} {
		if err := AcceptConfirmed(ctx, name); err != nil {
			t.Fatal(err)
		}
	}
	if len(accepted) != 1 || accepted[0] != "db" {
		t.Fatalf("accepted %v, want [db]", accepted)
	}
	if err := AcceptConfirmed(context.Background(), "app"); err != nil || len(accepted) != 1 {
		t.Fatalf("without a confirmation: %v, accepted %v", err, accepted)
	}
}
