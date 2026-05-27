-- 045_user_avatar.sql
-- Account page exposes a "Change avatar" button. Store the bytes
-- directly in SQLite — at the scale Dockmesh runs (tens to low
-- hundreds of users with sub-MB images) it's simpler than a
-- filesystem mount + backup-coordination story. The backup tarball
-- already covers DB content for free.

ALTER TABLE users ADD COLUMN avatar_blob BLOB;
ALTER TABLE users ADD COLUMN avatar_mime TEXT;
