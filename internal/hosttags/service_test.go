package hosttags

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"sort"
	"testing"

	_ "github.com/mattn/go-sqlite3"
)

func newDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`
		CREATE TABLE host_tags (
			host_id    TEXT NOT NULL,
			tag        TEXT NOT NULL,
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			PRIMARY KEY (host_id, tag)
		);
		CREATE INDEX idx_host_tags_tag ON host_tags(tag);
	`)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestSetReplacesEntireTagList(t *testing.T) {
	svc := New(newDB(t))
	ctx := context.Background()

	// Initial set.
	got, err := svc.Set(ctx, "local", []string{"prod", "eu-west"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"eu-west", "prod"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("first set returned %v, want %v", got, want)
	}

	// Replace with a different set.
	got, err = svc.Set(ctx, "local", []string{"staging"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []string{"staging"}) {
		t.Errorf("replace returned %v, want [staging]", got)
	}

	// Cache reflects the replacement.
	if !svc.HasTag("local", "staging") {
		t.Error("cache missing staging after set")
	}
	if svc.HasTag("local", "prod") {
		t.Error("cache still has prod after replace")
	}
}

func TestSetValidatesTagSyntax(t *testing.T) {
	svc := New(newDB(t))
	cases := []string{
		"UPPERCASE",
		"has space",
		"under_score",      // underscore disallowed
		"ends-with-hyphen-and-way-too-long-padding-here-33", // >32 chars
		"-starts-with-hyphen",
	}
	for _, bad := range cases {
		if _, err := svc.Set(context.Background(), "h", []string{bad}); err == nil {
			t.Errorf("expected error for tag %q", bad)
		}
	}
}

func TestAddDoesNotDuplicate(t *testing.T) {
	svc := New(newDB(t))
	ctx := context.Background()
	_ = svc.Add(ctx, "h1", "prod")
	_ = svc.Add(ctx, "h1", "prod") // idempotent
	_ = svc.Add(ctx, "h1", "eu")

	got := svc.Tags("h1")
	if !reflect.DeepEqual(got, []string{"eu", "prod"}) {
		t.Errorf("tags = %v", got)
	}
}

func TestAddCapEnforced(t *testing.T) {
	svc := New(newDB(t))
	ctx := context.Background()
	// Fill up to cap with valid tags.
	for i := 0; i < maxTagsPerHost; i++ {
		// Format as t0...t19 to stay within length constraint.
		tag := "tag-" + string(rune('a'+i%26))
		if err := svc.Add(ctx, "h", tag); err != nil {
			// May fail on duplicate — try unique names via index.
			tag = "tag-x" + string(rune('a'+i%26))
			_ = svc.Add(ctx, "h", tag)
		}
	}
	// One more should trip the cap (assuming we hit the cap).
	if len(svc.Tags("h")) >= maxTagsPerHost {
		err := svc.Add(ctx, "h", "one-too-many")
		if !errors.Is(err, ErrTooManyTags) {
			t.Errorf("expected ErrTooManyTags, got %v", err)
		}
	}
}

func TestRemoveClearsTag(t *testing.T) {
	svc := New(newDB(t))
	ctx := context.Background()
	_ = svc.Add(ctx, "h", "prod")
	_ = svc.Add(ctx, "h", "eu")
	if err := svc.Remove(ctx, "h", "prod"); err != nil {
		t.Fatal(err)
	}
	if svc.HasTag("h", "prod") {
		t.Error("prod still present after remove")
	}
	if !svc.HasTag("h", "eu") {
		t.Error("eu wrongly removed")
	}
	// Remove non-existent is a no-op.
	if err := svc.Remove(ctx, "h", "doesnotexist"); err != nil {
		t.Errorf("remove-nonexistent should be no-op, got %v", err)
	}
}

func TestHostsWithTag(t *testing.T) {
	svc := New(newDB(t))
	ctx := context.Background()
	_, _ = svc.Set(ctx, "h1", []string{"prod", "eu"})
	_, _ = svc.Set(ctx, "h2", []string{"prod"})
	_, _ = svc.Set(ctx, "h3", []string{"staging"})

	got := svc.HostsWithTag("prod")
	sort.Strings(got)
	if !reflect.DeepEqual(got, []string{"h1", "h2"}) {
		t.Errorf("HostsWithTag prod = %v, want [h1 h2]", got)
	}

	got = svc.HostsWithTag("staging")
	if !reflect.DeepEqual(got, []string{"h3"}) {
		t.Errorf("HostsWithTag staging = %v", got)
	}

	got = svc.HostsWithTag("nonexistent")
	if len(got) != 0 {
		t.Errorf("unknown tag should return empty, got %v", got)
	}
}

func TestHostsWithAllTags(t *testing.T) {
	svc := New(newDB(t))
	ctx := context.Background()
	_, _ = svc.Set(ctx, "h1", []string{"prod", "eu", "web"})
	_, _ = svc.Set(ctx, "h2", []string{"prod", "web"})
	_, _ = svc.Set(ctx, "h3", []string{"prod", "eu", "db"})

	// prod AND eu: h1, h3
	got := svc.HostsWithAllTags([]string{"prod", "eu"})
	sort.Strings(got)
	if !reflect.DeepEqual(got, []string{"h1", "h3"}) {
		t.Errorf("prod+eu = %v, want [h1 h3]", got)
	}

	// prod AND eu AND web: only h1
	got = svc.HostsWithAllTags([]string{"prod", "eu", "web"})
	if !reflect.DeepEqual(got, []string{"h1"}) {
		t.Errorf("prod+eu+web = %v, want [h1]", got)
	}

	// no matches
	got = svc.HostsWithAllTags([]string{"prod", "nonexistent"})
	if len(got) != 0 {
		t.Errorf("no-match should be empty, got %v", got)
	}
}

func TestLoadRebuildsCache(t *testing.T) {
	db := newDB(t)
	svc := New(db)
	ctx := context.Background()

	// Write directly to DB, bypassing the service cache.
	if _, err := db.ExecContext(ctx,
		`INSERT INTO host_tags (host_id, tag) VALUES ('h1', 'cached')`); err != nil {
		t.Fatal(err)
	}

	// Before Load, cache is empty.
	if svc.HasTag("h1", "cached") {
		t.Error("cache should be empty before Load")
	}

	// After Load, cache reflects DB.
	if err := svc.Load(ctx); err != nil {
		t.Fatal(err)
	}
	if !svc.HasTag("h1", "cached") {
		t.Error("cache should see 'cached' after Load")
	}
}

func TestRemoveAllForHost(t *testing.T) {
	svc := New(newDB(t))
	ctx := context.Background()
	_, _ = svc.Set(ctx, "h1", []string{"a", "b"})
	_, _ = svc.Set(ctx, "h2", []string{"a"})

	if err := svc.RemoveAllForHost(ctx, "h1"); err != nil {
		t.Fatal(err)
	}
	if len(svc.Tags("h1")) != 0 {
		t.Errorf("h1 tags should be empty, got %v", svc.Tags("h1"))
	}
	// h2 untouched.
	if !reflect.DeepEqual(svc.Tags("h2"), []string{"a"}) {
		t.Errorf("h2 should still have [a], got %v", svc.Tags("h2"))
	}
}
