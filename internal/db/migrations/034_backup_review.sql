-- P.13.2: backup defaults moved to opt-in. Existing installs that have
-- the auto-created `dockmesh-system` job from before this change get
-- flagged for operator review on next boot — the user picks "Keep"
-- (clear flag) or "Disable" (clear flag + flip enabled=0). New installs
-- never get a default job created in the first place; nothing here
-- targets them.
--
-- needs_review:  currently-pending review banner state (1=show, 0=hide)
-- review_reason: human-readable text shown next to the banner buttons
ALTER TABLE backup_jobs ADD COLUMN needs_review INTEGER NOT NULL DEFAULT 0;
ALTER TABLE backup_jobs ADD COLUMN review_reason TEXT NOT NULL DEFAULT '';
