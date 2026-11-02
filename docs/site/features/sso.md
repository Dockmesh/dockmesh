---
title: Single sign-on
description: Sign users in through OIDC, OAuth2, SAML or LDAP providers, map groups to roles, and the security behaviour you need to plan for.
---

Dockmesh supports four kinds of external identity providers. All of them are free and can be used side by side. Add them on the **Authentication** page under **01 · Identity providers** with **Add provider** (needs `system.update`). Pick a kind and, where offered, a template. Enabled providers show up as buttons or forms on `/login`.

| Kind | Typical providers | Templates in the dialog |
|---|---|---|
| OIDC | Keycloak, Authelia, Authentik, Azure AD, Google, GitLab | Keycloak, Authelia, Microsoft 365 (Azure AD), Google, GitLab, Generic OIDC |
| OAuth2 | GitHub, Gitea, Bitbucket, GitLab OAuth | GitHub, GitLab OAuth, Google OAuth, Generic OAuth2 |
| SAML | Azure AD, AD FS, Okta, Authentik | Azure AD (Microsoft Entra), AD FS, Okta, Authentik / Keycloak, Generic SAML 2.0 |
| LDAP | OpenLDAP, Active Directory, FreeIPA | none |

Every provider has a slug (used in its URLs), a display name, a **Default role** and an enabled flag. For OIDC, **Test discovery** in the dialog checks that the issuer URL answers. The other kinds have test endpoints in the API (`POST /api/v1/<kind>/providers/{id}/test`).

## Callback URLs

The URLs Dockmesh gives to providers are built from `DOCKMESH_BASE_URL`. Set it to the public URL users open in their browser, or redirects go to `http://localhost:8080`.

| Kind | Register at the provider |
|---|---|
| OIDC | `<base>/api/v1/auth/oidc/<slug>/callback` |
| OAuth2 | `<base>/api/v1/auth/oauth2/<slug>/callback` |
| SAML | ACS `<base>/api/v1/auth/saml/<slug>/acs`, SP metadata `<base>/api/v1/auth/saml/<slug>/metadata` |
| LDAP | none. Dockmesh binds to the directory itself |

The provider dialog shows the exact URL as **Callback URL** or **ACS URL (read-only)**.

## Configuration fields

**OIDC:** **Issuer URL** (Dockmesh fetches `/.well-known/openid-configuration` from it), **Client ID**, client secret, **Scopes** (must include `openid`; add `email` and, if needed, the scope that carries groups), **Group claim**. The login uses the authorization code flow with PKCE and a nonce. The ID token must contain an `email` claim. The username comes from `preferred_username`, then `name`, then the part of the e-mail before `@`.

**OAuth2:** **Authorization URL**, **Token URL**, **Userinfo URL**, **Client ID**, client secret, **Scopes**, **Subject field**, **Username field**, **Email field**, **Groups field** (all fields of the userinfo JSON). The subject field is the user id that the Dockmesh account is keyed by; leave it empty to use `sub`, or `id` when there is no `sub` (GitHub, GitLab and Gitea send `id`). Never use a login name or e-mail there: those can be renamed and later taken by someone else. The provider must return an e-mail address in the userinfo response. With GitHub that means the user needs a public e-mail, or the login fails.

**SAML:** **IdP metadata URL** or **IdP metadata XML**, **SP entity ID**, **Signing certificate (PEM)**, **Encryption certificate (PEM)** if the IdP encrypts assertions, **NameID format** (default `persistent`), an optional **Subject attribute**, and the **Username attribute**, **Email attribute** and **Groups attribute**. The account is keyed by the NameID, or by the subject attribute when one is set (for Azure AD the template uses the `objectidentifier` claim). Use a persistent NameID or an attribute that never changes; a transient NameID changes at every sign-in, so the user could not sign in again. The IdP answers with a cross-site POST, so with an IdP on another domain Dockmesh has to be served over HTTPS.

**LDAP:** **Host**, **Port**, **TLS mode** (`ldaps`, `starttls` or `none`), skip-verify, **Bind DN** and password for the service account, **User search base**, **User search filter** (`%s` is replaced by the login name), **Subject attribute**, **Username attribute**, **Email attribute**, **Group search base**, **Group membership attribute** (`memberOf` for Active Directory). Dockmesh binds as the service account, finds the user, then binds as the user to check the password. The account is keyed by the subject attribute; leave it empty to use `objectGUID` (Active Directory), else `entryUUID` (OpenLDAP), else the DN. Set it for directories that have neither, for example `nsUniqueId` (389 Directory Server) or `ipaUniqueID` (FreeIPA); the DN changes when a user is moved or renamed. The service account's password is stored encrypted when secrets encryption is on (`DOCKMESH_SECRETS_ENCRYPT_ENV`, default `true`).

## Roles from groups

<!-- update after: sso: link by email only when verified and allowed, keep manual roles -->
On every SSO sign-in, Dockmesh works out the user's role and overwrites the role stored on the account. A role you change by hand on the **Users** page lasts only until the user signs in again.

**SAML, OAuth2 and LDAP** use the group mapping list in the provider dialog: each entry maps a group value to a role. The user gets the default role, or the role of a mapped group they belong to. If a user is in several mapped groups, the last match in the order the provider sends them wins, so avoid overlapping mappings.

**OIDC** keeps only two entries of the group mapping list: on save, the first entry mapped to `admin` is stored as `admin_group` and the first mapped to `operator` as `operator_group`. Entries for other roles are discarded. The role comes from these fields:

| Field | Result |
|---|---|
| `admin_group` | Members of this group (in the **Group claim**) get `admin` |
| `operator_group` | Members get `operator`, unless they're also in `admin_group` |
| `default_role` | Everyone else (default `viewer`) |

Without an `admin` or `operator` mapping, every OIDC user gets the default role.

## How sign-ins find their account

Each SSO account is linked to one provider and one subject: the OIDC `sub`, the SAML NameID or subject attribute, the OAuth2 subject field, or the LDAP subject attribute. A sign-in enters the account whose provider and subject match. When none does:

- With **Link to existing accounts with the same verified email** on (OIDC and OAuth2), a local account with the same e-mail is linked, but only when the provider marks the address as verified and the account has no second factor, no role that manages users or settings, and no other provider link.
- Otherwise, if an account already uses the e-mail, the sign-in is refused.
- Otherwise a new account is created when **Auto-create accounts on first SSO sign-in** is on.

Links are bound to the identity provider they came from, because the same subject at another provider is another person:

- **Deleting a provider** unlinks its accounts. They stay as local accounts without a usable password, so a new provider with the same slug can't sign into them.
- **Pointing a provider at another identity provider** unlinks its accounts when you save: a different OIDC issuer, SAML metadata of another IdP entity, a different OAuth2 userinfo URL or subject field, or a different LDAP host, port or subject attribute. The provider dialog warns before you save. To re-attach the users, enable linking by verified e-mail or remove the old accounts and let them be created again.

### Re-keying after the upgrade to v0.5.0

Before v0.5.0, OAuth2 accounts were keyed by the username field (for example the GitHub `login`) and LDAP accounts by their DN. Both can be renamed and reused, so the upgrade moves these accounts to the immutable id. An account moves at its next sign-in that presents its old key, within 30 days of the upgrade. When the old key came from a field other than the new subject (a login name, a DN), the e-mail at the provider also has to match the account, so someone who took over a renamed login doesn't get the old account. On that path, accounts with two-factor authentication or a role that manages users or settings are never moved, and neither is a sign-in whose provider marks the e-mail as unverified.

Until then the **Authentication** page and a notice list the accounts that haven't moved yet. **End re-keying** stops waiting for them. Accounts that didn't move in time, accounts excluded above, and accounts whose user was renamed or moved before signing in again can no longer sign in through their provider; remove them and let them be created again. SAML accounts got the same one-time re-key with v0.4.2.

## Security notes

SSO sign-ins skip the Dockmesh second factor and the local account lockout. The provider is fully responsible for authenticating the user.

<!-- update after: http: trust forwarding headers only from configured proxies -->
The state cookies used during the redirect aren't marked `Secure`. Serve Dockmesh over HTTPS only.

What to do as an admin:

- Only connect providers where users can't change their own e-mail address, or where every address is verified.
- Enforce MFA at the provider, since Dockmesh won't ask for it.
- For LDAP, rely on your directory's lockout policy.
- Don't use the same e-mail on a local admin account and on a provider account that someone else could control.
- Check the role mappings before enabling a provider. Mapping a broad group to `admin` grants admin to everyone in it on their next sign-in.

## Provider recipes

These are starting points. Field names on the provider side differ between versions.

**Keycloak (OIDC):** Create a confidential OpenID Connect client in your realm and add `<base>/api/v1/auth/oidc/<slug>/callback` as a valid redirect URI. In Dockmesh, pick the Keycloak template and set **Issuer URL** to `https://<keycloak-host>/realms/<realm>`, plus the client ID and secret. For groups, add a group-membership mapper that writes a `groups` claim into the ID token, and set **Group claim** to `groups`.

**Authelia (OIDC):** Register a client in Authelia's `identity_providers.oidc.clients` with the Dockmesh callback as a redirect URI and the `openid`, `email`, `profile` and `groups` scopes. In Dockmesh, pick the Authelia template, set **Issuer URL** to Authelia's base URL, add `groups` to **Scopes** and set **Group claim** to `groups`.

**Authentik (OIDC):** Create an OAuth2/OpenID provider and an application for it, with the Dockmesh callback as redirect URI. Use the Generic OIDC template with the issuer URL Authentik shows for the provider (it ends in `/application/o/<application-slug>/`). Authentik's default scope mappings include `email` and a `groups` claim in the profile scope.

See also [Authentication](./authentication.md) and [Permissions](../concepts/permissions.md).
