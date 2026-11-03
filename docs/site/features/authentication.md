---
title: Authentication
description: Local sign-in, password policy, lockout and rate limits, sessions, two-factor authentication, trusted proxies and invites in Dockmesh.
---

Dockmesh has its own user accounts with passwords, and can also sign users in through [SSO providers](./sso.md). The instance-wide settings described here are under **Settings → Authentication** (needs Settings & audit: Manage on everything). Each user manages their own password, 2FA and sessions on the **My account** page, from the user menu at the bottom of the sidebar.

## Local sign-in

Users sign in on `/login` with username and password. Passwords are hashed with argon2id (64 MiB memory, 3 iterations, 4 threads). The first admin account is created by the setup wizard, or with `dockmesh admin create`. See [dockmesh CLI](../reference/dockmesh-cli.md).

## Password policy

Under **Local password policy**:

| Setting | Default |
|---|---|
| **Minimum length** | 8 |
| upper + lower, digit, symbol | off |
| **Lockout · max attempts** | 5 (0 turns the lockout off) |
| **Lockout · duration (min)** | 15 |

The policy applies when a password is set or changed. An admin can set a new password for a user with **Reset password…** on the user's **Sessions & 2FA** tab, or with `dockmesh admin reset-password`.

## Lockout and rate limits

After the configured number of failed sign-ins in a row, the account is locked for the configured time. Wrong passwords and wrong 2FA codes both count. Unlock an account early with **Unlock** on the user's **Sessions & 2FA** tab under **Users & roles**, or with `dockmesh admin unlock --user <name>`.

Independent of that policy, Dockmesh limits failed attempts. The per-username counter of an existing account is stored with the account, survives a restart and is cleared by **Unlock** or `dockmesh admin unlock`. The per-address counters, and the counters for usernames that match no account, live in memory and a restart resets them.

| Limit | Blocks for |
|---|---|
| 10 failed password or 2FA attempts for one username within 15 minutes | 15 minutes, that username |
| 100 failed password sign-ins from one client address within 15 minutes | 15 minutes, that address |
| 20 failed 2FA codes from one client address within 15 minutes | 15 minutes, that address |
| 10 failed LDAP sign-ins per address and per username within 15 minutes | 15 minutes |
| 5 wrong codes when turning off your own 2FA within 15 minutes | 15 minutes |

Unknown usernames are counted and answered the same way as existing ones, and take as long to reject as a wrong password. IPv6 clients are counted per `/64`.

## Client address and trusted proxies

The client address goes into rate limits, the session list and the audit log. Dockmesh uses the address of the TCP connection, and believes `X-Forwarded-For`, `X-Real-IP` and `X-Forwarded-Proto` only when that connection comes from a trusted proxy. By default only loopback (`127.0.0.0/8`, `::1`) is trusted, so a reverse proxy on the same machine works without configuration.

If your proxy runs elsewhere, add its addresses under **Settings → General → Trusted proxies**: comma-separated IPs or CIDRs. `DOCKMESH_TRUSTED_PROXIES` in `dockmesh.env` sets the same list when the field is empty. Only add addresses that are really your proxy: whoever connects from them can choose the client address Dockmesh records.

When a peer that isn't trusted sends `X-Forwarded-For` or `X-Real-IP`, Dockmesh ignores the headers and shows admins a notice suggesting to configure trusted proxies. `X-Forwarded-For` is read from the right, skipping trusted hops. If `X-Forwarded-For` and `X-Real-IP` name different addresses, neither is believed. `X-Forwarded-Proto: https` from a trusted proxy marks the request as HTTPS, which turns on `Secure` cookies and HSTS.

## Sessions

| Property | Default | Setting under **Sessions & sign-in flow** |
|---|---|---|
| Access token | 15 minutes | fixed |
| Session lifetime | 24 hours, extended on every token refresh | **Absolute lifetime (h)** |
| Idle timeout | 60 minutes without a refresh (0 turns it off) | **Idle timeout (min)** |
| Sessions per user | 20, the least recently used ends first (0 means no limit) | **Max sessions per user** |

Revoked and expired sessions are deleted after 30 days.

The web UI refreshes its access token in the background. Each refresh token is single-use. Using one twice revokes the whole session, which protects against stolen refresh tokens. Browser tabs of the same Dockmesh share one refresh, so several open tabs don't sign you out, and a short network outage during a refresh keeps you signed in and retries. Every request checks that the session still exists and reads the user's current assignments, so revoking a session or changing access takes effect immediately.

Under **Sessions** on the **My account** page you see your sessions with browser, IP and last activity, and can revoke any of them. Changing your own password revokes your other sessions. An admin password reset revokes all of the user's sessions. The **Sign out everywhere** button on the **My account** page is disabled; use `POST /api/v1/sessions/revoke-all` instead.

Admins see another user's sessions on that user's **Sessions & 2FA** tab under **Users & roles** and can end one or all of them. See [Permissions](../concepts/permissions.md#scopes) for who may.

## Two-factor authentication

Users turn on TOTP under **Security** on the **My account** page:

1. Click **Enable 2FA** and scan the QR code with an authenticator app, or paste the secret.
2. Enter a code to confirm.
3. Save the 10 recovery codes shown. They're displayed once. Each works once in place of a TOTP code.

From then on, sign-in asks for a code after the password. A code is accepted only once, with one 30-second step of clock skew either way. Turning 2FA off needs a current TOTP or recovery code. An admin can turn off a user's 2FA with **Turn off 2FA…** on the user's **Sessions & 2FA** tab (needs Users & roles: Manage on everything), or with `DELETE /api/v1/users/{id}/mfa`.

The TOTP secret is stored unencrypted in the database. Protect the database file and its backups.

Sign-ins through SSO providers don't ask for the Dockmesh second factor. See [SSO](./sso.md#security-notes).

## Sign-in options that don't exist

Password rotation and reuse rules, remember-me, requiring 2FA for admins, turning off local password sign-in and self-registration are not available. The API under `/api/v1/auth/policy` and `/api/v1/auth/signin` accepts and stores some of these fields (`rotation_days`, `require_tfa_for_admin`, `allow_local_password`, `allow_self_register`, `session_remember_me_days`, `password_forbid_reuse_count`), but nothing enforces them. If you need these controls, enforce them at your identity provider or with a proxy in front of Dockmesh.

**Auto-create accounts on first SSO sign-in** is enforced: with it off, SSO users without an account are refused.

## Invites

On **Users & roles**, **Invite** creates one-time links for a list of e-mail addresses, all with one role on one scope. The e-mail addresses are only a label; Dockmesh doesn't send e-mail. Copy each link and pass it on. The recipient opens `/invite/<token>`, picks a username and password, and gets the role on the scope from the invite.

The dialog offers 1, 3 (default) or 7 days until the link expires. The API (`POST /api/v1/invites`) uses 24 hours without `ttl_hours` and caps it at 7 days. Creating invites needs Users & roles: Manage on everything, and you can only invite with a role and scope you could assign yourself. Open invites are listed on the **Invites** tab, where you can revoke them.

See also [API tokens](./api-tokens.md), [Permissions](../concepts/permissions.md) and [Security hardening](../operations/hardening.md).
