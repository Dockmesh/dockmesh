-- sso_immutable_subjects
-- OAuth2 and LDAP accounts are keyed by an immutable id: a configurable
-- subject field (default: the userinfo "sub", else "id") and a subject
-- attribute (default: objectGUID, else entryUUID, else the DN).
ALTER TABLE oauth2_providers ADD COLUMN subject_field TEXT NOT NULL DEFAULT '';
ALTER TABLE ldap_providers ADD COLUMN subject_attribute TEXT NOT NULL DEFAULT '';

-- Existing links were keyed by the username field or the DN, which can be
-- renamed and reused. They no longer match directly; for 30 days the next
-- sign-in that presents the old key moves the account to its new subject.
ALTER TABLE users ADD COLUMN oauth2_legacy_subject TEXT;
ALTER TABLE users ADD COLUMN oauth2_rekey_until DATETIME;
ALTER TABLE users ADD COLUMN ldap_legacy_subject TEXT;
ALTER TABLE users ADD COLUMN ldap_rekey_until DATETIME;
UPDATE users SET oauth2_legacy_subject = oauth2_subject, oauth2_subject = NULL,
                 oauth2_rekey_until = datetime('now', '+30 days')
 WHERE oauth2_provider IS NOT NULL AND oauth2_subject IS NOT NULL;
UPDATE users SET ldap_legacy_subject = ldap_subject, ldap_subject = NULL,
                 ldap_rekey_until = datetime('now', '+30 days')
 WHERE ldap_provider IS NOT NULL AND ldap_subject IS NOT NULL;

-- Links left behind by deleted providers would attach to a new provider
-- that reuses the slug.
UPDATE users SET oidc_provider = NULL, oidc_subject = NULL
 WHERE oidc_provider IS NOT NULL AND oidc_provider NOT IN (SELECT slug FROM oidc_providers);
UPDATE users SET saml_provider = NULL, saml_subject = NULL, saml_rekey_until = NULL
 WHERE saml_provider IS NOT NULL AND saml_provider NOT IN (SELECT slug FROM saml_providers);
UPDATE users SET oauth2_provider = NULL, oauth2_subject = NULL,
                 oauth2_legacy_subject = NULL, oauth2_rekey_until = NULL
 WHERE oauth2_provider IS NOT NULL AND oauth2_provider NOT IN (SELECT slug FROM oauth2_providers);
UPDATE users SET ldap_provider = NULL, ldap_subject = NULL,
                 ldap_legacy_subject = NULL, ldap_rekey_until = NULL
 WHERE ldap_provider IS NOT NULL AND ldap_provider NOT IN (SELECT slug FROM ldap_providers);
