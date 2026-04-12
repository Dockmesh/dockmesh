-- Tamper-evidence hash chain for the audit log (concept §15.10).
-- Each new row stores:
--   prev_hash : row_hash of the previous row (or the genesis hash for row #1)
--   row_hash  : SHA-256 of prev_hash + canonical(fields)
-- Pre-existing rows from Phase 1 are left NULL — the chain starts at the
-- first row written after this migration, seeded by a genesis entry.
ALTER TABLE audit_log ADD COLUMN prev_hash TEXT;
ALTER TABLE audit_log ADD COLUMN row_hash  TEXT;
