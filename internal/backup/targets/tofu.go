package targets

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"

	"golang.org/x/crypto/ssh"

	"github.com/dockmesh/dockmesh/internal/notices"
)

// ErrNoPendingHostKey is returned when accepting a host key while none is
// waiting for approval.
var ErrNoPendingHostKey = errors.New("no changed host key is waiting for approval")

// KeyRecorder persists the trust-on-first-use state of one SFTP config.
type KeyRecorder interface {
	// RecordHostKey stores key unless a key is already stored; it reports
	// whether this call stored it and the key stored now.
	RecordHostKey(ctx context.Context, key string) (bool, string, error)
	SetPendingHostKey(ctx context.Context, key string) error
}

// TOFU wires an SFTP target to a KeyRecorder: the first key is recorded,
// a different one later refuses the connection, raises a critical notice
// and waits as the pending key for an admin to accept.
type TOFU struct {
	Rec     KeyRecorder
	Notices *notices.Service
	ID      string // stable id for notice keys, e.g. "job:12"
	Label   string // what the notice names, e.g. "backup job nightly"
	Link    string
}

func RecordedNoticeKey(id string) string { return "backups.sftp-host-key-recorded:" + id }
func ChangedNoticeKey(id string) string  { return "backups.sftp-host-key-changed:" + id }

// Attach installs the check on t if it is an SFTP target.
func (tf TOFU) Attach(ctx context.Context, t Target) {
	if sf, ok := t.(*SFTP); ok {
		sf.OnHostKey(func(ev HostKeyEvent) error {
			return tf.Handle(context.WithoutCancel(ctx), ev)
		})
	}
}

// Handle stores and announces one host key event.
func (tf TOFU) Handle(ctx context.Context, ev HostKeyEvent) error {
	if ev.Kind == "recorded" {
		// Conditional: two first connections racing with different keys
		// can't both win; the loser is treated as a changed key.
		won, stored, err := tf.Rec.RecordHostKey(ctx, ev.Key)
		if err != nil {
			return err
		}
		if !won && stored != ev.Key {
			ev.Kind = "changed"
			ev.OldFingerprint = stored
			if old, _, _, _, perr := ssh.ParseAuthorizedKey([]byte(stored)); perr == nil {
				ev.OldFingerprint = ssh.FingerprintSHA256(old)
			}
		}
	}
	var notice error
	switch ev.Kind {
	case "recorded":
		notice = tf.raise(ctx, notices.RaiseInput{
			Key:      RecordedNoticeKey(tf.ID),
			Audience: notices.Admins(),
			Severity: notices.SevInfo,
			Title:    "SFTP host key recorded for " + tf.Label,
			Body: fmt.Sprintf("The first connection recorded the server's host key %s. From now on "+
				"connections to a server that presents a different key are refused.", ev.Fingerprint),
			Link: tf.Link,
		})
	case "matched":
		if tf.Notices != nil {
			notice = tf.Notices.Resolve(ctx, RecordedNoticeKey(tf.ID))
		}
	case "changed":
		if err := tf.Rec.SetPendingHostKey(ctx, ev.Key); err != nil {
			return err
		}
		notice = tf.raise(ctx, notices.RaiseInput{
			Key:      ChangedNoticeKey(tf.ID),
			Audience: notices.Admins(),
			Severity: notices.SevCritical,
			Title:    "SFTP host key changed for " + tf.Label,
			Body: fmt.Sprintf("The server used to present %s and now presents %s, so connections are "+
				"refused. If the server was reinstalled or its key rotated on purpose, accept the new "+
				"key; otherwise someone may be intercepting the connection.", ev.OldFingerprint, ev.Fingerprint),
			Link: tf.Link,
		})
	}
	if notice != nil {
		slog.Warn("sftp host key notice", "target", tf.Label, "err", notice)
	}
	if ev.Kind == "changed" {
		return &HostKeyChangedError{Old: ev.OldFingerprint, New: ev.Fingerprint}
	}
	return nil
}

func (tf TOFU) raise(ctx context.Context, in notices.RaiseInput) error {
	if tf.Notices == nil {
		return nil
	}
	return tf.Notices.Raise(ctx, in)
}

// keyQueries keep the host key inside a JSON config column.
type keyQueries struct{ record, stored, pending, accept string }

var jobKeyQueries = keyQueries{
	record: `UPDATE backup_jobs SET target_config = json_set(target_config, '$.host_key', ?),
		updated_at = CURRENT_TIMESTAMP
		WHERE id = ? AND COALESCE(json_extract(target_config, '$.host_key'), '') = ''`,
	stored: `SELECT json_extract(target_config, '$.host_key') FROM backup_jobs WHERE id = ?`,
	pending: `UPDATE backup_jobs SET target_config = json_set(target_config, '$.host_key_pending', ?),
		updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
	accept: `UPDATE backup_jobs SET target_config = json_remove(
		json_set(target_config, '$.host_key', json_extract(target_config, '$.host_key_pending')), '$.host_key_pending'),
		updated_at = CURRENT_TIMESTAMP
		WHERE id = ? AND COALESCE(json_extract(target_config, '$.host_key_pending'), '') != ''`,
}

var targetKeyQueries = keyQueries{
	record: `UPDATE backup_targets SET config_json = json_set(config_json, '$.host_key', ?),
		updated_at = CURRENT_TIMESTAMP
		WHERE id = ? AND COALESCE(json_extract(config_json, '$.host_key'), '') = ''`,
	stored: `SELECT json_extract(config_json, '$.host_key') FROM backup_targets WHERE id = ?`,
	pending: `UPDATE backup_targets SET config_json = json_set(config_json, '$.host_key_pending', ?),
		updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
	accept: `UPDATE backup_targets SET config_json = json_remove(
		json_set(config_json, '$.host_key', json_extract(config_json, '$.host_key_pending')), '$.host_key_pending'),
		updated_at = CURRENT_TIMESTAMP
		WHERE id = ? AND COALESCE(json_extract(config_json, '$.host_key_pending'), '') != ''`,
}

// ColumnRecorder is the KeyRecorder of one backup job or saved target row.
type ColumnRecorder struct {
	db *sql.DB
	q  keyQueries
	id int64
}

// JobKeyRecorder records in backup_jobs.target_config.
func JobKeyRecorder(db *sql.DB, jobID int64) ColumnRecorder {
	return ColumnRecorder{db: db, q: jobKeyQueries, id: jobID}
}

// TargetKeyRecorder records in backup_targets.config_json.
func TargetKeyRecorder(db *sql.DB, targetID int64) ColumnRecorder {
	return ColumnRecorder{db: db, q: targetKeyQueries, id: targetID}
}

func (r ColumnRecorder) RecordHostKey(ctx context.Context, key string) (bool, string, error) {
	res, err := r.db.ExecContext(ctx, r.q.record, key, r.id)
	if err != nil {
		return false, "", err
	}
	if n, _ := res.RowsAffected(); n == 1 {
		return true, key, nil
	}
	var stored sql.NullString
	err = r.db.QueryRowContext(ctx, r.q.stored, r.id).Scan(&stored)
	return false, stored.String, err
}

func (r ColumnRecorder) SetPendingHostKey(ctx context.Context, key string) error {
	_, err := r.db.ExecContext(ctx, r.q.pending, key, r.id)
	return err
}

// AcceptPendingHostKey makes the pending key the trusted one.
func (r ColumnRecorder) AcceptPendingHostKey(ctx context.Context) error {
	res, err := r.db.ExecContext(ctx, r.q.accept, r.id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNoPendingHostKey
	}
	return nil
}

// BuildChecked builds a stored target with the host-key check attached.
func (s *TargetStore) BuildChecked(ctx context.Context, t *StoredTarget, n *notices.Service) (Target, error) {
	built, err := Build(t.Type, t.Config)
	if err != nil {
		return nil, err
	}
	TOFU{
		Rec:     TargetKeyRecorder(s.db, t.ID),
		Notices: n,
		ID:      fmt.Sprintf("target:%d", t.ID),
		Label:   "backup target " + t.Name,
		Link:    "/backups",
	}.Attach(ctx, built)
	return built, nil
}

// AcceptHostKey trusts the changed host key a stored target last saw.
func (s *TargetStore) AcceptHostKey(ctx context.Context, id int64, n *notices.Service) error {
	if err := TargetKeyRecorder(s.db, id).AcceptPendingHostKey(ctx); err != nil {
		return err
	}
	if n != nil {
		return n.Resolve(ctx, ChangedNoticeKey(fmt.Sprintf("target:%d", id)))
	}
	return nil
}

// KeepHostKey returns newCfg with the host-key fields taken from the
// stored config: kept while the SFTP server (host and port) stays the
// same, dropped otherwise. Clients never set them, so editing a target
// can neither plant a trusted key nor reset the check for the same server.
func KeepHostKey(oldType string, oldCfg any, newType string, newCfg any) any {
	if newType != "sftp" {
		return newCfg
	}
	next := map[string]any{}
	if err := decodeConfig(newCfg, &next); err != nil {
		return newCfg
	}
	delete(next, "host_key")
	delete(next, "host_key_pending")
	if oldType == "sftp" {
		var prev, cur SFTPConfig
		if decodeConfig(oldCfg, &prev) == nil && decodeConfig(next, &cur) == nil &&
			prev.Host == cur.Host && portOr22(prev.Port) == portOr22(cur.Port) {
			if prev.HostKey != "" {
				next["host_key"] = prev.HostKey
			}
			if prev.HostKeyPending != "" {
				next["host_key_pending"] = prev.HostKeyPending
			}
		}
	}
	return next
}

func portOr22(p int) int {
	if p == 0 {
		return 22
	}
	return p
}
