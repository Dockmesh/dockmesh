-- An SSO login may attach itself to an existing account by email only when
-- the provider allows it (off by default) and the IdP verified the email.
-- A role set by an admin stays put on SSO logins; roles that came from the
-- IdP keep following its group mapping, which is how every account that
-- already signed in through SSO behaves today.
ALTER TABLE oidc_providers ADD COLUMN allow_email_link INTEGER NOT NULL DEFAULT 0;
ALTER TABLE saml_providers ADD COLUMN allow_email_link INTEGER NOT NULL DEFAULT 0;
ALTER TABLE oauth2_providers ADD COLUMN allow_email_link INTEGER NOT NULL DEFAULT 0;
ALTER TABLE ldap_providers ADD COLUMN allow_email_link INTEGER NOT NULL DEFAULT 0;

ALTER TABLE users ADD COLUMN role_source TEXT NOT NULL DEFAULT 'manual';
UPDATE users SET role_source = 'sso'
 WHERE oidc_provider IS NOT NULL OR saml_provider IS NOT NULL
    OR oauth2_provider IS NOT NULL OR ldap_provider IS NOT NULL;
