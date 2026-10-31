package auth

import (
	"database/sql"
	"testing"
	"time"
)

func TestActiveLockEnd(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	future := now.Add(time.Minute)
	past := now.Add(-time.Minute)
	later := now.Add(time.Hour)

	tests := []struct {
		name  string
		lock  sql.NullTime
		limit int64
		want  *time.Time
	}{
		{"none", sql.NullTime{}, 0, nil},
		{"expired", sql.NullTime{Time: past, Valid: true}, past.Unix(), nil},
		{"policy lock", sql.NullTime{Time: future, Valid: true}, 0, &future},
		{"limit block", sql.NullTime{}, future.Unix(), &future},
		{"later wins", sql.NullTime{Time: future, Valid: true}, later.Unix(), &later},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := activeLockEnd(now, tc.lock, tc.limit)
			if (got == nil) != (tc.want == nil) || (got != nil && !got.Equal(*tc.want)) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}
