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
- Before applying database schema changes, Dockmesh now writes a copy of the database next
  to it and keeps the newest three.

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

### Upgrade notes
- [re-login, all] Everyone is signed out once by the upgrade and has to sign in again. The
  sign-in page says why.
- [legacy-api-tokens, admins] API tokens created before this version have no owner. They keep
  working and are marked "legacy" under API tokens; recreate each one while signed in as the
  user who should own it, then revoke the old one. A notice stays up until none are left.
- [premigrate-snapshot, admins] Make sure the database directory has room for a full copy
  of the database. Before schema changes Dockmesh writes `<db>.pre-migrate-<timestamp>.bak`
  next to the database (`dockmesh.db.pre-migrate-20261029T101500Z.bak`, UTC) and keeps the
  newest three. If the copy cannot be written, Dockmesh does not start and does not change
  the database; set `DOCKMESH_SKIP_PREMIGRATE_BACKUP=1` to upgrade without it. To roll back,
  stop the service, copy the `.bak` file over the database file, delete the database's
  `-wal` and `-shm` files if present, and start the previous version.
