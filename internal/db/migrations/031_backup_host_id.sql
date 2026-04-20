-- Multi-host backup (P.12, fixes FINDING-33):
-- backup_jobs now carry a host_id so an operator can back up volumes
-- and stacks from a specific remote agent rather than always hitting
-- the central daemon. Empty / 'local' = central dockmesh host.
ALTER TABLE backup_jobs ADD COLUMN host_id TEXT NOT NULL DEFAULT '';
