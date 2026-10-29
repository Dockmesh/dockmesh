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

**OAuth2:** **Authorization URL**, **Token URL**, **Userinfo URL**, **Client ID**, client secret, **Scopes**, **Username field**, **Email field**, **Groups field** (all fields of the userinfo JSON). The provider must return an e-mail address in the userinfo response. With GitHub that means the user needs a public e-mail, or the login fails.

**SAML:** **IdP metadata URL** or **IdP metadata XML**, **SP entity ID**, **Signing certificate (PEM)**, **Encryption certificate (PEM)** if the IdP encrypts assertions, **NameID format**, and the **Username attribute**, **Email attribute** and **Groups attribute**.

**LDAP:** **Host**, **Port**, **TLS mode** (`ldaps`, `starttls` or `none`), skip-verify, **Bind DN** and password for the service account, **User search base**, **User search filter** (`%s` is replaced by the login name), **Username attribute**, **Email attribute**, **Group search base**, **Group membership attribute** (`memberOf` for Active Directory). Dockmesh binds as the service account, finds the user, then binds as the user to check the password. The service account's password is stored encrypted when secrets encryption is on (`DOCKMESH_SECRETS_ENCRYPT_ENV`, default `true`).

## Roles from groups

<!-- update after: sso: link by email only when verified and allowed, keep manual roles -->
On every SSO sign-in, Dockmesh works out the user's role and overwrites the role stored on the account. A role you change by hand on the **Users** page lasts only until the user signs in again.

**SAML, OAuth2 and LDAP** use the group mapping list in the provider dialog: each entry maps a group value to a role. The user gets the default role, or the role of a mapped group they belong to. If a user is in several mapped groups, the last match in the order the provider sends them wins, so avoid overlapping mappings.

**OIDC** ignores the group mapping list, even though the dialog shows it. Its role comes from three provider fields only:

| Field | Result |
|---|---|
| `admin_group` | Members of this group (in the **Group claim**) get `admin` |
| `operator_group` | Members get `operator`, unless they're also in `admin_group` |
| `default_role` | Everyone else (default `viewer`) |

`admin_group` and `operator_group` aren't in the dialog. Set them through the API with `PUT /api/v1/oidc/providers/{id}`. Without them, every OIDC user gets the default role.

## Security notes

<!-- update after: sso: link by email only when verified and allowed, keep manual roles -->
When an SSO sign-in comes in, Dockmesh looks for an existing account with the same provider subject or the same e-mail address. It doesn't check whether the provider verified that address. If a provider lets users set their own e-mail, someone can sign in there with an admin's address and take over the matching Dockmesh account, including its role until the next mapping overwrites it. Accounts that don't exist yet are always created, regardless of the **Auto-create accounts on first SSO sign-in** toggle.

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
