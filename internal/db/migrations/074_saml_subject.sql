-- SAML accounts are keyed by a stable subject: the NameID in the format the
-- provider requests, or the value of a configured attribute.
--
-- Earlier versions requested a transient NameID, so the subject stored for
-- existing SAML accounts changes on every login. Each of them may be keyed
-- once more within 90 days by its provider and email (saml_rekey_until).
ALTER TABLE saml_providers ADD COLUMN subject_attribute TEXT NOT NULL DEFAULT '';
ALTER TABLE users ADD COLUMN saml_rekey_until DATETIME;
UPDATE users SET saml_rekey_until = datetime('now', '+90 days') WHERE saml_provider IS NOT NULL;
