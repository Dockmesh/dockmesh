-- An SSO login may attach itself to an existing account by email only when
-- the provider allows it (off by default) and the IdP verified the email.
-- A role set by an admin stays put on SSO logins; roles that came from the
-- IdP keep following its group mapping. Existing SSO-linked accounts start
-- as 'sso'.
ALTER TABLE oidc_providers ADD COLUMN allow_email_link INTEGER NOT NULL DEFAULT 0;
ALTER TABLE saml_providers ADD COLUMN allow_email_link INTEGER NOT NULL DEFAULT 0;
ALTER TABLE oauth2_providers ADD COLUMN allow_email_link INTEGER NOT NULL DEFAULT 0;
ALTER TABLE ldap_providers ADD COLUMN allow_email_link INTEGER NOT NULL DEFAULT 0;

ALTER TABLE users ADD COLUMN role_source TEXT NOT NULL DEFAULT 'manual';
UPDATE users SET role_source = 'sso'
 WHERE oidc_provider IS NOT NULL OR saml_provider IS NOT NULL
    OR oauth2_provider IS NOT NULL OR ldap_provider IS NOT NULL;

-- Before this change any SSO login could attach to an account by matching
-- email alone, so a link may have been made by someone else's IdP account.
-- Ask admins to review SSO-linked accounts, admins first.
INSERT INTO notices (key, audience, audience_value, severity, title, body, link)
SELECT 'sso-linked-accounts-review', 'admins', '', 'warning',
       'Review accounts linked to single sign-on',
       'Earlier versions linked SSO logins to existing accounts by email without checking that the email was verified, so a link could have been made by someone else. Check that these accounts belong to the right people.'
       || char(10) || n || ' account(s) linked to SSO.'
       || CASE WHEN admins <> '' THEN char(10) || 'Admins among them:' || char(10) || admins ELSE '' END,
       '/users'
  FROM (SELECT COUNT(*) AS n,
               COALESCE(group_concat(CASE WHEN role = 'admin' THEN '- ' || username END, char(10)), '') AS admins
          FROM users WHERE role_source = 'sso')
 WHERE n > 0;
