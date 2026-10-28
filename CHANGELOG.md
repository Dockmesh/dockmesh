# Changelog

Notable changes to Dockmesh, loosely following [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).
Every user-visible pull request adds a line under `[Unreleased]`. Pull requests users won't
notice (CI, refactoring, tests) carry the `no-changelog` label instead. Releases up to and
including v0.3.3 predate this file; their notes are on the
[GitHub releases page](https://github.com/Dockmesh/dockmesh/releases).

Release notes on GitHub and the `release-manifest.json` asset of each release are generated
from this file by `scripts/relmanifest`, so it follows a strict format:

- A release header is `## [X.Y.Z] - YYYY-MM-DD [severity]`, where severity is `security`
  (update as soon as possible), `recommended` or `normal`. To cut a release, rename
  `[Unreleased]` to the new header and add a fresh empty `[Unreleased]` above it.
- Text between the header and the first `###` is the release summary. An optional line
  `Minimum upgrade from: X.Y.Z` there names the oldest version that can upgrade directly.
- Subsections are `### Added`, `### Changed`, `### Fixed`, `### Security` and
  `### Upgrade notes`; each holds `- ` bullets (indented lines continue a bullet).
- A Security bullet of the form `- GHSA-xxxx-xxxx-xxxx (critical|high|moderate|low): Title`
  is published as an advisory.
- Every Upgrade notes bullet is something a person has to do after upgrading and starts with
  `[id, audience]`, where `id` is a short kebab-case name that stays fixed and audience is
  `all` or `admins`: `- [sftp-host-key, admins] Re-save each SFTP backup target.`

## [Unreleased]

### Added
- Each release now ships a `release-manifest.json` with its severity, advisories and
  required upgrade steps, and its release notes are taken from this changelog.
- When something needs your attention after an upgrade, Dockmesh now shows it as a banner
  and at the top of the notifications drawer, with a link to where you fix it. The notice
  goes away on its own once the issue is fixed; non-critical ones can be hidden from the
  banner.

### Changed
- API tokens now belong to the user who created them. A token never grants more than its
  role and its owner's current permissions and host scope allow, and it stops working when
  the owner is deleted. A token created with another token expires no later than it, and a
  token can only revoke itself.
- Turning off two-factor authentication, or replacing the authenticator, now asks for a
  current code or a recovery code; after five wrong codes it is blocked for 15 minutes. Admins
  can no longer use the user-management reset on their own account.
- Changing your password signs out your other sessions; an admin password reset (in the UI
  or with `dockmesh admin reset-password`) signs out all of the user's sessions.
- A role or scope change now applies to a signed-in user's next request instead of after up
  to 15 minutes.
- Downloading a backup archive needs the new "Download backups" permission, which the
  built-in Admin and Host Admin roles have; Host Admins only for jobs on their own hosts.
  System backups and the backup key export are admin-only. Viewer, Operator and Deployer
  can still see backup status but no longer download archives. Creating, editing or running
  a backup job that includes the server itself is admin-only too, and other jobs need their
  host within your scope.
- Browsing, reading and downloading files inside a container now needs "Shell into
  container", the same permission as uploading files or opening a terminal.
- The built-in Host Admin role no longer edits system settings, global environment
  variables, password and sign-in policy or single sign-on providers.
- Saving a user without `scope_tags` keeps the user's host scope instead of clearing it.

### Security
- Each kind of signed token (session, refresh, two-factor challenge, WebSocket ticket, single
  sign-on state) now has its own key and is only accepted for its own purpose. Before, a
  half-finished two-factor login could be used to turn two-factor authentication off.
- WebSocket connections for container exec, logs, stats and events now run with the
  permissions and host scope of the user who opened them, and each ticket works only once.
  Before, a user restricted to some stacks or hosts could open a shell in any container.
  Open connections close when the session is signed out or the permission is taken away,
  and exec sessions are now recorded in the audit log.
- Signing in with an unknown username takes as long as with a wrong password, and invalid
  API tokens can no longer tie up the server with expensive hash checks.
- Creating or editing users, invites and roles can no longer grant permissions or host scope
  the editor doesn't have, and nobody can change or remove a user or role with more rights
  than their own. Before, anyone allowed to edit users could make themselves admin. The last
  account that can administer Dockmesh can no longer be demoted or deleted, and an invite
  can no longer be accepted twice.
- Users restricted to some hosts or stacks no longer see other hosts' or stacks' data:
  container details (including environment variables), compose files, stack status, volumes,
  networks, images, host metrics and the live event stream are now filtered by scope, and
  container checks look at the host the container actually runs on.
- Backup archives could be downloaded by every role, including Viewer, and a system backup
  contains every secret of the installation. Downloads now need their own permission and are
  recorded in the audit log.
- Passwords, keys and tokens of backup targets and notification channels, and webhook URLs
  that act as credentials, are no longer returned by the API. Forms show them as set; leaving
  a field empty keeps the stored value, unless the server, URL or user changes, in which
  case the secret has to be entered again. Only unscoped users can change or delete roles,
  and an invite stops working once its creator could no longer grant it.

### Upgrade notes
- [re-login, all] Everyone is signed out once by the upgrade and has to sign in again. The
  sign-in page says why.
- [legacy-api-tokens, admins] API tokens created before this version have no owner. They keep
  working and are marked "legacy" under API tokens; recreate each one while signed in as the
  user who should own it, then revoke the old one. A notice stays up until none are left.
- [custom-role-permissions, admins] Custom roles that could restore backups keep downloading
  archives; other custom roles lose archive downloads and container file access, and lose
  system backups and the backup key unless they are admins. A notice lists the affected
  roles; grant "Download backups" or "Shell into container" under Users → Roles where intended.
- [host-admin-system-settings, admins] Host Admins no longer change system settings, SSO or
  global environment variables. Give the Admin role to anyone who still needs that.
