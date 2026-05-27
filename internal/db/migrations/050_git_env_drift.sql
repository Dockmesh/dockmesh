-- Stack git-source: persist last sync's env-merge drift so the UI can
-- show a "drift detected" banner without re-diffing client-side.
-- last_env_drift is the JSON-encoded EnvDrift struct from
-- internal/gitsource/envmerge.go; NULL when no sync has run or when
-- the last sync produced no drift.
ALTER TABLE stack_git_sources ADD COLUMN last_env_drift TEXT;
