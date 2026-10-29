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
- Host enrollment tokens now expire after 24 hours (`DOCKMESH_AGENT_ENROLL_TOKEN_TTL` changes
  this) and work exactly once. "Re-issue token" on a host now re-enrolls that host under the
  same ID, so its stacks, tags and backup jobs stay attached. It now needs the `hosts.create`
  permission instead of `hosts.update`, plus host scope for the host.
- The agent installer picks the binary for the host's CPU architecture and checks its
  SHA-256. If the server has no binary for that architecture, the installer stops with a
  clear message instead of installing the wrong one.
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
- New agents enroll over the agent port and check the server's certificate against the
  Dockmesh agent CA, whose fingerprint is part of the install command. Before, an agent
  trusted any server during enrollment, so someone on the network path could take it over.
  Enrolling over plain HTTP now needs the agent's `--insecure-enroll` flag.
- Agents now download self-upgrades from the agent port over their authenticated
  connection and check them against a SHA-256 the server sends before replacing the binary. Automatic upgrades stop after
  three failed attempts for the same build instead of retrying every minute, and two
  upgrades can no longer write the new binary at the same time.
- Agents check every stack and volume name the server sends with the server's own rules
  before touching files or mounting a volume.

### Upgrade notes
- [agent-upgrade, admins] Hosts that are already enrolled keep working and do not need to be
  re-enrolled. Upgrade each agent once from its host page (or use automatic upgrades) so it
  verifies future upgrades; the upgrade from an older agent is still downloaded the old,
  unverified way.
- [re-login, all] Everyone is signed out once by the upgrade and has to sign in again. The
  sign-in page says why.
- [legacy-api-tokens, admins] API tokens created before this version have no owner. They keep
  working and are marked "legacy" under API tokens; recreate each one while signed in as the
  user who should own it, then revoke the old one. A notice stays up until none are left.
