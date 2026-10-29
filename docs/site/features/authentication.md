---
title: Authentication
description: Local sign-in, password policy, lockout, sessions, two-factor authentication and invites in Dockmesh.
---

Dockmesh has its own user accounts with passwords, and can also sign users in through [SSO providers](./sso.md). The settings described here are on the **Authentication** page (needs `system.update`), and each user manages their own security on the **Account** page.

## Local sign-in

Users sign in on `/login` with username and password. Passwords are hashed with argon2id (64 MiB memory, 3 iterations, 4 threads). The first admin account is created by the setup wizard, or with `dockmesh admin create`. See [dockmesh CLI](../reference/dockmesh-cli.md).

## Password policy

Under **02 · Local password policy**:

| Setting | Default |
|---|---|
| **Minimum length** | 8 |
| Require upper + lower case, digit, symbol | off |
| **Lockout · max attempts** | 5 (0 disables lockout) |
| **Lockout · duration (min)** | 15 |

The policy applies when a password is set or changed. **Rotation (days)** is stored but not enforced: nobody is asked to change their password when it's due.

After the configured number of failed sign-ins, the account is locked for the configured time. Unlock it early with `dockmesh admin unlock --user <name>`. Unknown user names take as long to reject as wrong passwords. Separately, `POST /api/v1/auth/login` is rate-limited per client IP: 10 failures within a minute block that address for 5 minutes.

<!-- update after: http: trust forwarding headers only from configured proxies -->
The client IP is taken from `X-Forwarded-For`, `X-Real-IP` or `True-Client-IP` without checking where the request came from, so a client can set it itself. That weakens the IP rate limit and puts unverified addresses into the session list and audit log. Account lockout isn't affected.

## Sessions

| Property | Default | Setting |
|---|---|---|
| Access token | 15 minutes | fixed |
| Session lifetime | 24 hours, extended on every token refresh | **Absolute lifetime (h)** |
| Idle timeout | 60 minutes without a refresh | **Idle timeout (min)** |
| Sessions per user | 20, oldest by last activity is revoked first | **Max sessions per user** |

The web UI refreshes its access token in the background. Each refresh token is single-use. Using one twice revokes the whole session, which protects against stolen refresh tokens. Every request checks that the session still exists and reads the user's current role, so revoking a session or changing a role takes effect immediately.

<!-- update after: web: share session refresh across tabs, fix stale page loads -->
With Dockmesh open in several browser tabs, two tabs can refresh at the same moment. The server then sees the refresh token used twice and signs you out. Keeping one tab open avoids this.

Under **03 · Sessions** on the **Account** page you see your sessions with browser, IP and last activity, and can revoke any of them. Changing your own password revokes your other sessions. An admin password reset revokes all of the user's sessions. **Sign out everywhere** on the Account page is disabled. Use `POST /api/v1/sessions/revoke-all` instead.

## Two-factor authentication

Users turn on TOTP under **02 · Security** on the **Account** page:

1. Click **Enable 2FA** and scan the QR code with an authenticator app.
2. Enter a code to confirm.
3. Save the 10 recovery codes shown. They're displayed once. Each works once in place of a TOTP code.

From then on, sign-in asks for a code after the password. Turning 2FA off needs a current TOTP or recovery code. An admin can reset a user's 2FA with `DELETE /api/v1/users/{id}/mfa`. There's no button for it.

<!-- update after: http: trust forwarding headers only from configured proxies -->
The TOTP secret is stored unencrypted in the database, and a code can be used more than once within its 30-second window. The code step (`POST /api/v1/auth/mfa`) isn't rate-limited, and wrong codes don't count towards the account lockout, so someone who knows a password can keep guessing codes. Protect the database file, and rate-limit `/api/v1/auth/login` and `/api/v1/auth/mfa` at your reverse proxy (see [Security hardening](../operations/hardening.md)).

Sign-ins through SSO providers don't ask for the Dockmesh second factor. See [SSO](./sso.md#security-notes).

## Sign-in settings that aren't enforced

<!-- update after: auth: hide sign-in settings that aren't enforced -->
The **03 · Sessions & sign-in flow** section shows these settings, saves them, and then ignores them:

| Setting | What actually happens |
|---|---|
| **Require 2FA for admin role** | Admins can sign in without TOTP |
| **Allow local password sign-in** | Password sign-in keeps working when off |
| **Auto-create accounts on first SSO sign-in** | SSO always creates missing accounts |
| **Allow self-registration** | There's no registration page |
| **Remember-me (days)** | The login page doesn't send a remember-me flag |

The password-reuse count (API only) is in the same state. If you need any of these controls, enforce them at your identity provider or with a proxy in front of Dockmesh.

## Invites

On the **Users** page, **Invite user** creates one-time links for a list of e-mail addresses, each with a role and optional host-tag scope. Dockmesh doesn't send e-mail. Copy each link and pass it on. The recipient opens `/invite/<token>`, picks a username and password, and gets the role and scope from the invite.

Links expire after 24 hours. The API accepts `ttl_hours` up to 7 days, and the expiry choice in the dialog isn't applied. Creating invites needs `users.create`. The role in an invite isn't checked against the inviter's own permissions, so only give `users.create` to people who may hand out any role.

See also [API tokens](./api-tokens.md), [Permissions](../concepts/permissions.md) and [Security hardening](../operations/hardening.md).
