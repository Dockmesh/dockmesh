package scanner

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// Store persists the latest scan report per image reference.
type Store struct {
	db *sql.DB
}

func NewStore(db *sql.DB) *Store { return &Store{db: db} }

// Save upserts the report for rep.Image. Existing rows for the same
// image_ref are replaced so we always hold the latest result.
func (s *Store) Save(ctx context.Context, rep *Report) error {
	if rep == nil || rep.Image == "" {
		return errors.New("empty report")
	}
	b, err := json.Marshal(rep.Vulnerabilities)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO scan_results
			(image_ref, scanner, scanner_version, findings_json,
			 critical, high, medium, low, negligible, unknown_sev, scanned_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(image_ref) DO UPDATE SET
			scanner = excluded.scanner,
			scanner_version = excluded.scanner_version,
			findings_json = excluded.findings_json,
			critical = excluded.critical,
			high = excluded.high,
			medium = excluded.medium,
			low = excluded.low,
			negligible = excluded.negligible,
			unknown_sev = excluded.unknown_sev,
			scanned_at = excluded.scanned_at
	`,
		rep.Image, rep.Scanner, rep.ScannerVersion, string(b),
		rep.Summary.Critical, rep.Summary.High, rep.Summary.Medium,
		rep.Summary.Low, rep.Summary.Negligible, rep.Summary.Unknown,
		rep.ScannedAt,
	)
	return err
}

// Get returns the cached report for an image ref, or nil if there is none.
func (s *Store) Get(ctx context.Context, imageRef string) (*Report, error) {
	var (
		rep        Report
		findings   string
		scanner    string
		scannerVer sql.NullString
		scannedAt  time.Time
		sum        Summary
	)
	err := s.db.QueryRowContext(ctx, `
		SELECT scanner, scanner_version, findings_json,
			critical, high, medium, low, negligible, unknown_sev, scanned_at
		FROM scan_results WHERE image_ref = ?`, imageRef).
		Scan(&scanner, &scannerVer, &findings,
			&sum.Critical, &sum.High, &sum.Medium,
			&sum.Low, &sum.Negligible, &sum.Unknown,
			&scannedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	rep.Image = imageRef
	rep.Scanner = scanner
	if scannerVer.Valid {
		rep.ScannerVersion = scannerVer.String
	}
	rep.ScannedAt = scannedAt
	rep.Summary = sum
	if err := json.Unmarshal([]byte(findings), &rep.Vulnerabilities); err != nil {
		return nil, err
	}
	return &rep, nil
}
