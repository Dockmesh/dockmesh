-- 056_invite_tokens.sql
-- One-time invite links for adding users without an SMTP dependency
-- (Coolify-style flow). Admin generates a link, copies it out, shares
-- it via whatever channel the team uses. Recipient hits the public
-- /invite/{token} page, picks a username + password, lands as a user.
--
-- token_hash stores the SHA-256 of the raw token so a database leak
-- doesn't hand over still-valid invites. The raw token is shown to the
-- admin exactly once at creation; if they lose it they have to
-- generate a new one.
--
-- email_hint is a UX nicety — operators usually invite a specific
-- person, so the accept page renders "Invitation for alice@example.com"
-- and the recipient knows they hit the right link. The email itself is
-- NOT used for any auth check.

CREATE TABLE IF NOT EXISTS invite_tokens (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    token_hash      TEXT    NOT NULL UNIQUE,
    role            TEXT    NOT NULL,
    -- JSON array of scope tags, e.g. ["team-platform","host:prod-eu"].
    -- Empty list = unscoped (no host filtering applied).
    scope_tags_json TEXT    NOT NULL DEFAULT '[]',
    email_hint      TEXT    NOT NULL DEFAULT '',
    expires_at      DATETIME NOT NULL,
    used_at         DATETIME,
    -- created_by stores the inviting user's id so the audit log can
    -- attribute the invite. We keep the row even after used_at is set
    -- so the admin can see "alice@… accepted on Y, invited by X".
    created_by      TEXT    NOT NULL,
    created_at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_invite_tokens_expires ON invite_tokens(expires_at);
