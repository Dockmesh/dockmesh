-- notice_blocking
-- Only blocking notices go to the banner. Every critical notice raised so
-- far stops something from working, so open ones become blocking; a
-- blocking notice can be hidden for a while (hidden_until) and comes back.
ALTER TABLE notices ADD COLUMN blocking INTEGER NOT NULL DEFAULT 0;
UPDATE notices SET blocking = 1 WHERE severity = 'critical';
ALTER TABLE notice_dismissals ADD COLUMN hidden_until DATETIME;
