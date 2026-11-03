---
title: Single sign-on
description: Sign users in through OIDC, OAuth2, SAML or LDAP providers, map groups to roles on scopes, and the security behaviour you need to plan for.
---

Dockmesh supports four kinds of external identity providers for single sign-on (SSO). All of them are free and can be used side by side. Add them under **Settings → Authentication** in the **Identity providers** section with **Add provider** (needs Settings & audit: Manage on everything). Pick a kind and, where offered, a template. Enabled providers show up as buttons or forms on `/login`.

| Kind | Typical providers | Templates in the dialog |
|---|---|---|
| OIDC | Keycloak, Authelia, Authentik, Azure AD, Google, GitLab | Keycloak, Authelia, Microsoft 365 (Azure AD), Google, GitLab, Generic OIDC |
| OAuth2 | GitHub, Gitea, Bitbucket, GitLab OAuth | GitHub, GitLab OAuth, Google OAuth, Generic OAuth2 |
| SAML | Azure AD, AD FS, Okta, Authentik | Azure AD (Microsoft Entra), AD FS, Okta, Authentik / Keycloak, Generic SAML 2.0 |
| LDAP | OpenLDAP, Active Directory, FreeIPA | none; **Directory mode** presets OpenLDAP or Active Directory |

Every provider has a slug (used in its URLs), a display name, a **Default role** and an enabled flag. For OIDC, **Test discovery** in the dialog checks that the issuer URL answers. The other kinds have test endpoints in the API (`POST /api/v1/<kind>/providers/{id}/test`).

## Callback URLs

The URLs Dockmesh gives to providers are built from `DOCKMESH_BASE_URL` in `dockmesh.env`. Set it to the public URL users open in their browser, or redirects go to `http://localhost:8080`. The **Base URL** field under **Settings → General** doesn't change these URLs; set the environment variable and restart.

| Kind | Register at the provider |
|---|---|
| OIDC | `<base>/api/v1/auth/oidc/<slug>/callback` |
| OAuth2 | `<base>/api/v1/auth/oauth2/<slug>/callback` |
| SAML | ACS `<base>/api/v1/auth/saml/<slug>/acs`, SP metadata and entity ID `<base>/api/v1/auth/saml/<slug>/metadata` |
| LDAP | none. Dockmesh binds to the directory itself |

The **Callback URLs** box on the Authentication page and the provider dialog (**Callback URL**, **ACS URL (read-only)**) show these URLs with the base the server uses. While `DOCKMESH_BASE_URL` is unset, the box warns that providers send users back to `http://localhost:8080`.

## Configuration fields

**OIDC:** **Issuer URL** (Dockmesh fetches `/.well-known/openid-configuration` from it), **Client ID**, **Client secret**, **Scopes** (must include `openid`; the default is `openid profile email`; add the scope that carries groups if needed), **Group claim**. The sign-in uses the authorization code flow with PKCE and a nonce. The ID token must contain an `email` claim. The username comes from `preferred_username`, then `name`, then the part of the email before `@`. The account is keyed by the issuer's `sub`.

**OAuth2:** **Authorization URL**, **Token URL**, **Userinfo URL**, **Client ID**, **Client secret**, **Scopes**, **Subject field**, **Username field**, **Email field**, **Groups field** (all fields of the userinfo JSON). The subject field is the immutable user id the Dockmesh account is keyed by; leave it empty to use `sub`, or `id` when there is no `sub` (GitHub, GitLab and Gitea send `id`). Never use a login name or email there: those can be renamed and later taken by someone else. The provider must return an email address in the userinfo response. With GitHub that means the user needs a public email, or the sign-in fails.

**SAML:** **IdP metadata XML** (paste the IdP's `<EntityDescriptor>`), **NameID format**, an optional **Subject attribute (optional)**, and the **Username attribute** (default `NameID`), **Email attribute** (default `email`) and **Groups attribute** (default `groups`). Dockmesh generates its own service-provider key and certificate and publishes them in the SP metadata; the SP entity ID is the SP metadata URL. The dialog also shows a **Metadata URL** tab and fields for an SP entity ID and signing and encryption certificates, but these values aren't saved: only pasted metadata XML is used.

The account is keyed by the NameID, or by the subject attribute when one is set. The Azure AD template sets it to `http://schemas.microsoft.com/identity/claims/objectidentifier`. **NameID format** offers `persistent` (default), `unspecified` and `emailAddress`, and Dockmesh asks the IdP for that format. A persistent NameID or an attribute that never changes is the safe choice: an `emailAddress` NameID changes when the user's email does, and the account then no longer matches. An assertion must carry exactly one value of the subject attribute.

The IdP answers with a cross-site POST. With an IdP on another domain, Dockmesh has to be reached over HTTPS: the state cookie then goes out with `SameSite=None; Secure`. Over plain HTTP only an IdP on the same site works, and the dialog shows a warning.

**LDAP:** **Host**, **Port**, **TLS mode** (LDAPS, StartTLS or none), **Skip TLS verification**, **Bind DN** and **Bind password** for the service account, **User search base**, **User search filter** (`%s` is replaced by the login name), **Subject attribute**, **Username attribute**, **Email attribute**, **Group search base**, **Group membership attribute** (`memberOf` for Active Directory). Dockmesh binds as the service account, finds the user, then binds as the user to check the password. With TLS mode none every password crosses the network in clear, and **Skip TLS verification** lets anyone in the path read them; use them only for tests. The account is keyed by the subject attribute; leave it empty to use `objectGUID` (Active Directory), else `entryUUID` (OpenLDAP), else the DN. Set it for directories that have neither, for example `nsUniqueId` (389 Directory Server) or `ipaUniqueID` (FreeIPA); the DN changes when a user is moved or renamed. Never use `uid` or `mail`.

Client secrets and the LDAP bind password are stored encrypted when secrets encryption is on (`DOCKMESH_SECRETS_ENCRYPT_ENV`, default `true`).

## Roles from groups

Each provider has a **Group → role mappings** list (**SAML attribute → role mappings** for SAML). Every mapping gives members of a group a role on a scope (**Role applies on**: everything, a host, a host tag or a stack). A user in several mapped groups gets all of those assignments. With no matching group, the user gets the provider's **Default role** on everything.

At every sign-in Dockmesh replaces the account's identity provider assignments with the ones its groups give now. Assignments you added by hand stay. To stop sign-ins from changing an account's access, use **Stop updating from groups** on the user's **Overview** tab under **Users & roles**.

The groups come from the **Group claim** (OIDC), the **Groups field** (OAuth2), the **Groups attribute** (SAML) or the **Group membership attribute** (LDAP). LDAP mappings match the full DN or only the group's CN, so `Admins` matches `cn=Admins,ou=groups,…`; the other kinds compare exactly.

Saving a provider needs that you could grant its default role and every mapping yourself. If a sign-in would remove the last admin assignment on everything, Dockmesh keeps it and tells admins in a notice. See [Permissions](../concepts/permissions.md#identity-providers).

## How sign-ins find their account

Each SSO account is linked to one provider and one subject: the OIDC `sub`, the SAML NameID or subject attribute, the OAuth2 subject field, or the LDAP subject attribute. A sign-in enters the account whose provider and subject match. When none does:

- If a local account already uses the email, it is linked only when the provider has **Link to existing accounts with the same verified email** on, the provider marks the address as verified, and the account has no second factor, no role that manages users or settings, and no other provider link. Otherwise the sign-in is refused. The option exists for OIDC and OAuth2. OIDC counts an address as verified when the ID token has `email_verified: true`; OAuth2 only when the **Email field** is `email` and the userinfo has `email_verified: true`, which GitHub doesn't send. SAML and LDAP addresses never count as verified.
- Otherwise a new account is created when **Auto-create accounts on first SSO sign-in** (under **Sessions & sign-in flow**) is on, and the sign-in is refused when it is off.

Links are bound to the identity provider they came from, because the same subject at another provider is another person:

- **Deleting a provider** unlinks its accounts. They stay as local accounts without a usable password, so a new provider with the same slug can't sign into them.
- **Pointing a provider at another identity provider** unlinks its accounts when you save: a different OIDC issuer, SAML metadata of another IdP entity, a different OAuth2 userinfo URL or subject field, or a different LDAP host, port or subject attribute. The provider dialog warns before you save. To re-attach the users, enable linking by verified email or remove the old accounts and let them be created again.

### Re-keying after the upgrade to v0.5.0

Before v0.5.0, OAuth2 accounts were keyed by the username field (for example the GitHub `login`) and LDAP accounts by their DN. Both can be renamed and reused, so the upgrade moves these accounts to the immutable id. An account moves at its next sign-in that presents its old key, within 30 days of the upgrade. When the old key came from a field other than the new subject (a login name, a DN), the email at the provider also has to match the account, so someone who took over a renamed login doesn't get the old account. On that path, accounts with two-factor authentication or a role that manages users or settings are never moved, and neither is a sign-in whose provider marks the email as unverified.

Until then the **Authentication** page and a notice list the accounts that haven't moved yet. **End re-keying** stops waiting for them. Accounts that didn't move in time, accounts excluded above, and accounts whose user was renamed or moved before signing in again can no longer sign in through their provider; remove them and let them be created again. SAML accounts got the same one-time re-key with v0.4.2.

## Security notes

SSO sign-ins skip the Dockmesh second factor and the local account lockout. The provider is fully responsible for authenticating the user. LDAP sign-ins are limited to 10 failures per client address and per username within 15 minutes.

The state cookies used during the redirect are `HttpOnly` and get the `Secure` flag when the request came over HTTPS (directly, or through a [trusted proxy](./authentication.md#client-address-and-trusted-proxies) that sends `X-Forwarded-Proto: https`) or when the base URL starts with `https://`. Serve Dockmesh over HTTPS.

What to do as an admin:

- Only connect providers where users can't change their own email address, or where every address is verified.
- Enforce MFA at the provider, since Dockmesh won't ask for it.
- For LDAP, rely on your directory's lockout policy as well.
- Don't use the same email on a local admin account and on a provider account that someone else could control.
- Check the group mappings and the default role before enabling a provider. Mapping a broad group to Admin on everything grants admin to everyone in it at their next sign-in.

## Provider recipes

These are starting points. Field names on the provider side differ between versions.

**Keycloak (OIDC):** Create a confidential OpenID Connect client in your realm and add `<base>/api/v1/auth/oidc/<slug>/callback` as a valid redirect URI. In Dockmesh, pick the Keycloak template and set **Issuer URL** to `https://<keycloak-host>/realms/<realm>`, plus the client ID and secret. For groups, add a group-membership mapper that writes a `groups` claim into the ID token (on the client's dedicated scope), and set **Group claim** to `groups`. The template puts `groups` into **Scopes**; Keycloak rejects the sign-in with `invalid_scope` unless the realm has a client scope named `groups` assigned to the client, so create that client scope or remove `groups` from **Scopes**.

**Authelia (OIDC):** Register a client in Authelia's `identity_providers.oidc.clients` with the Dockmesh callback as a redirect URI and the `openid`, `email`, `profile` and `groups` scopes. In Dockmesh, pick the Authelia template and set **Issuer URL** to Authelia's base URL. The template already requests the `groups` scope, and **Group claim** defaults to `groups`.

**Authentik (OIDC):** Create an OAuth2/OpenID provider and an application for it, with the Dockmesh callback as redirect URI. Use the Generic OIDC template with the issuer URL Authentik shows for the provider (it ends in `/application/o/<application-slug>/`). Authentik's default scope mappings include `email` and a `groups` claim in the profile scope.

See also [Authentication](./authentication.md) and [Permissions](../concepts/permissions.md).
