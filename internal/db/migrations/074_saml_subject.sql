-- SAML accounts are keyed by a stable subject: the NameID in the format the
-- provider requests, or the value of a configured attribute.
-- Existing SAML accounts may take their new subject once within 90 days,
-- matched by provider and email.
ALTER TABLE saml_providers ADD COLUMN subject_attribute TEXT NOT NULL DEFAULT '';
ALTER TABLE users ADD COLUMN saml_rekey_until DATETIME;
UPDATE users SET saml_rekey_until = datetime('now', '+90 days') WHERE saml_provider IS NOT NULL;
