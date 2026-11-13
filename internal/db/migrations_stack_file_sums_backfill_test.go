package db

import (
	"slices"
	"testing"
)

func TestStackFileSumsBackfillMarksTrackedStacks(t *testing.T) {
	t.Parallel()
	database, _ := openFileDB(t)
	if err := migrateUpTo(database, "20261109150513_notice_alert_text_keys.sql"); err != nil {
		t.Fatal(err)
	}
	for _, row := range [][2]string{{"web", "compose.yaml"}, {"web", ".env"}, {"web", "@override"}, {"only-override", "@override"}} {
		if _, err := database.Exec(`INSERT INTO stack_file_sums (stack, file, sum) VALUES (?, ?, 'x')`, row[0], row[1]); err != nil {
			t.Fatal(err)
		}
	}
	if err := Migrate(database); err != nil {
		t.Fatal(err)
	}
	rows, err := database.Query(`SELECT stack FROM stack_file_sums WHERE file = '@backfill' ORDER BY stack`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var marked []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		marked = append(marked, s)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(marked, []string{"only-override", "web"}) {
		t.Errorf("marked = %v, want [only-override web]", marked)
	}
}
