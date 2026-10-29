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
  `all` or `admins`: `- [re-login, all] Sign in again after the upgrade.`

## [Unreleased]

### Added
- Settings → System → Trusted proxies (or `DOCKMESH_TRUSTED_PROXIES`) lists the reverse
  proxies in front of Dockmesh. Only these may tell Dockmesh the client address or that the
  connection was HTTPS.
- Each release now ships a `release-manifest.json` with its severity, advisories and
  required upgrade steps, and its release notes are taken from this changelog.
- When something needs your attention after an upgrade, Dockmesh now shows it as a notice
  with a link to where you fix it. The page shows one banner for the most urgent open notice
  (Critical before Action required); "+N more notices" opens the full list below it, and
  the notifications drawer lists them too. Info notices only appear in the list and the
  drawer. A notice goes away on its own once the issue is fixed; non-critical ones can be
  hidden from the banner.
- Before applying database schema changes, Dockmesh now writes a copy of the database next
  to it and keeps the newest three.

### Changed
- Host enrollment tokens now expire after 24 hours (`DOCKMESH_AGENT_ENROLL_TOKEN_TTL` changes
  this) and work exactly once. "Re-issue token" on a host now re-enrolls that host under the
  same ID, so its stacks, tags and backup jobs stay attached. It now needs the `hosts.create`
  permission instead of `hosts.update`, plus host scope for the host.
- The agent installer picks the binary for the host's CPU architecture and checks its
  SHA-256. If the server has no binary for that architecture, the installer stops with a
  clear message instead of installing the wrong one.
- The Authentication page no longer shows sign-in settings Dockmesh doesn't enforce yet:
  password rotation and reuse, remember-me, requiring 2FA for admins, turning off local
  passwords and self-registration. Saved values are kept.
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
- Migrating a stack that uses named volumes to another host is refused with an explanation,
  including in the pre-flight checks. Volume data isn't copied yet, so such a migration used
  to finish with empty volumes on the new host.
- Rotating the secrets key keeps every previous key in `secrets.age-key.history`, and the
  exported backup key file contains them all, so backups made before a rotation can still be
  restored. An interrupted rotation no longer leaves the server without a usable key.

### Security
- Dockmesh is now built with Go 1.27.3, and its Go dependencies (including the SSH, HTTP/2,
  Git, JWT, SAML signature and Docker client libraries) are updated to versions that fix
  the known vulnerabilities in code Dockmesh calls. The previous release was built with Go
  1.25.0, which no longer receives security fixes.
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
  connection and check them against a SHA-256 the server sends before replacing the binary.
  Automatic upgrades stop after three failed attempts for the same build instead of retrying
  every minute, and two
  upgrades can no longer write the new binary at the same time.
- Agents check every stack and volume name the server sends with the server's own rules
  before touching files or mounting a volume.
- `X-Forwarded-For` and `X-Real-IP` are only believed from a trusted proxy (localhost unless
  configured). Before, anyone could set them to dodge the login rate limit and put a fake
  address into sessions and the audit log.
- Failed logins are now limited per account: after 10 failures within 15 minutes the
  username is blocked for 15 minutes, also when the lockout policy is turned off and also
  for usernames that don't exist. The limit per client address is raised to 100 failures in
  15 minutes, so users behind one proxy or NAT don't lock each other out. Wrong two-factor
  codes now count toward the lockout and the account limit like wrong passwords, a locked
  account can no longer finish a two-factor login, and a correct password alone no longer
  resets the failed-attempt counter. Unlocking a user (in the UI or with
  `dockmesh admin unlock`) clears the account limit too, and each block is written to the
  audit log as `auth.lockout`. The sign-in page now says how long to wait when a login is
  blocked.
- Two-factor codes, LDAP sign-in, invite links and session refresh are now rate-limited per
  client address and, where there is one, per account. Each two-factor code works only once.
- The web UI is served with a Content Security Policy that only allows Dockmesh's own
  scripts, and with headers against clickjacking and MIME sniffing. HSTS is sent when
  Dockmesh is reached over HTTPS.
- Single sign-on state cookies are marked `Secure` when Dockmesh is reached over HTTPS, and an
  error passed back by an identity provider can no longer inject parameters into the
  redirect to the sign-in page.
- The password of the first admin account is printed once to the console instead of being
  written to the structured log.
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
- Single sign-on no longer attaches a login to an existing account just because the email
  matches. Linking needs the provider's new "link to existing accounts" option and an email the
  provider marks as verified (OIDC and OAuth2). A role an admin set by hand is no longer
  overwritten by the provider's group mapping on the next login, and turning off "create
  accounts on first SSO login" now really stops new accounts.
- SAML sign-in now asks the identity provider for the NameID format set on the provider
  (persistent by default) and keys the account by it, or by a subject attribute such as the
  Microsoft Entra object ID. Before,
  Dockmesh asked for a transient NameID, which changes on every login. A SAML response is now
  accepted only once and only as the answer to a sign-in that this server started.
- Passwords, keys and tokens of backup targets and notification channels, and webhook URLs
  that act as credentials, are no longer returned by the API. Forms show them as set; leaving
  a field empty keeps the stored value, unless the server, URL or user changes, in which
  case the secret has to be entered again. Only unscoped users can change or delete roles,
  and an invite stops working once its creator could no longer grant it.
- A user allowed to restore backups could restore a system backup into a volume and read the
  database and secrets from it. Restoring a system backup into a volume or stack is no longer
  possible; system backups are restored only with `dockmesh restore` on the server. Verifying
  system backups, reading their run log, and turning on, editing, running or deleting the
  system backup job are admin-only. Restoring, verifying and reading the log of other backups
  needs the job's host within your scope.
- Backup jobs whose names differ only in case or punctuation stored their archives in the
  same folder, so the retention of one job could delete another job's backups, including the
  system backups. Such names are now rejected, and the name `dockmesh-system` is kept for
  the system backup job. On S3, retention no longer touches jobs whose name merely starts
  with the same text.
- Volume browsing, system restore and Git-synced stacks no longer follow symlinks out of the
  volume, the data directory or the repository. Before, a symlink in a volume or repository,
  or a crafted backup archive, could read or overwrite files on the host.
- A Git source's `path_in_repo` must stay inside the repository.
- Restoring a volume backup only accepts a Docker volume name as the target; a path such as
  `/etc` was mounted from the host and overwritten. Saving a backup job whose volume source
  is a path instead of a volume name is refused as well.
- Uploads to the backup verification endpoint are limited to 1 GiB.
- Backup hooks run commands inside containers, so creating or changing a job with hooks now
  needs the permission to exec into containers, and every hook container must be within your
  host and stack scope.
- Local backup targets can only write below allowed directories (setting
  `backups.local_roots`, which only admins can change). The directories existing local
  targets use were added automatically.
- SFTP backup targets now check the server's host key: the first connection records it, and a
  different key later stops the job until an admin accepts it. Existing jobs keep running. The
  same applies to saved SFTP targets, used for the audit log archive and the target test.
- When a stack's `compose.yaml` or `.env` changes outside Dockmesh, for example through a
  container that mounts the stack directory, the stack page says so and the next deploy or
  rollback asks for confirmation. Git syncs of such a stack neither copy the new commit nor
  deploy it, and users who can deploy stacks get a notice naming the changed files; deploying
  the stack from its page confirms the changes, the notice goes away and the next sync picks
  up the commit.
- A backup job set to encrypt no longer writes an unencrypted archive when secrets encryption
  is turned off; the run fails and admins get a notice.
- Git webhooks now need a webhook secret and a valid signature. Before, anyone who knew a
  stack name could trigger a sync, and with auto-deploy a deploy, without any secret. New
  webhook secrets must be at least 16 characters, and repeated deliveries no longer start
  overlapping syncs of the same stack.

### Fixed
- Requests that a client cancelled while they were reading the database (a closed browser tab,
  a dropped connection) could leave the database connection failing every later query until the
  restart, so sign-in, host enrollment and most pages answered with errors. The SQLite driver is
  updated, which fixes this.
- Notices and other short database writes could fail with "database is locked" while other
  requests were writing. The notice about an untrusted proxy then never appeared until the next
  restart. The database connection now waits for the lock from the start.
- SAML sign-in with an identity provider on another domain failed with "missing state cookie",
  because the browser did not send the cookie along with the provider's answer. Over HTTPS the
  cookie now goes along; over plain HTTP the sign-in page explains that HTTPS is needed.
- Turning on the daily system backup created a job with the relative path `./data/backups`,
  which a server started by systemd resolved to `/data/backups`, so the backups failed. New
  system backup jobs write to `backups` in the data directory. An existing system backup job
  with a relative path keeps the directory it already wrote archives to, now as an absolute
  path, or moves to the new default if it has none there; admins get a notice with the new
  path. No archives are moved. System backups leave out the `backups` folder of the data
  directory, so an archive no longer contains all earlier ones.
- Relative bind mounts such as `./html:/usr/share/nginx/html` now point at the stack's own
  directory. They used to point at a temporary directory that was deleted after the deploy, so
  the container saw an empty folder and whatever it wrote there was lost. On the first redeploy
  of an affected stack, files the old container wrote are copied into the stack directory if
  it is still empty. On remote hosts the directory starts empty, because files next to
  `compose.yaml` aren't copied to agents yet. A bind of the whole stack directory (`.:/app`)
  mounts it read-write like docker compose does, and the stack page warns that the container
  can change `compose.yaml` and `.env`.
- The setup wizard's connection test only reaches public addresses and this server's own
  address on its web port, 80 or 443, so it can no longer be used to probe the internal
  network, local services or cloud metadata.
  The wizard runs one install at a time, its check endpoints stop answering when the setup
  window closes, and it reopens only when no account can administer Dockmesh (custom roles
  included). It no longer adds an existing system account to the `docker` group; only an
  account the wizard creates itself is added.
- Reinstalling over an existing install no longer reopens the setup wizard to anyone who can
  reach the server. `DOCKMESH_SETUP_FORCE=true` in the env file is now ignored once an admin
  account exists, and admins get a notice to remove it; the wizard also refuses to finish if
  an admin was created in the meantime. The installer only writes that flag on a host without
  an earlier install, keeps a copy of an existing env file as `dockmesh.env.bak` (or
  `dockmesh.env.bak.<timestamp>.<n>` if a backup is already there), and refuses to write
  through symlinks in the data directory.

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
- [trusted-proxies, admins] If Dockmesh runs behind a reverse proxy on another machine or in
  a container, add the proxy's address under Settings → System → Trusted proxies. Until
  then every user appears with the proxy's address in sessions and the audit log, and they
  share one per-address login limit of 100 failures in 15 minutes. Dockmesh shows a notice
  when it sees such a proxy.
- [custom-role-permissions, admins] Custom roles that could restore backups keep downloading
  archives; other custom roles lose archive downloads and container file access, and lose
  system backups and the backup key unless they are admins. A notice lists the affected
  roles; grant "Download backups" or "Shell into container" under Users → Roles where intended.
- [sso-email-linking, admins] Someone who has a local account and signs in through SSO for the
  first time now gets "an account with this email already exists". Turn on "link to existing
  accounts" for that provider (OIDC/OAuth2 with verified emails), or remove the local account.
  Accounts with two-factor authentication or the Admin role are never linked this way. Accounts
  that already signed in through SSO keep working as long as the provider sends the same
  subject; users whose LDAP DN or OAuth2 login changed need their old account removed or
  relinked by an admin.
- [saml-subject, admins] SAML accounts are now keyed by a stable subject. New providers ask
  for a persistent NameID; existing providers keep their NameID format. Check it under
  Authentication before your users sign in again (persistent is recommended, and with
  emailAddress a changed email breaks the login) or set a subject attribute: the first sign-in
  after the upgrade fixes the subject an account is known by. Accounts created through SAML
  before this version get their new subject at their next sign-in, once, when it comes from
  the same provider with the same email and within 30 days of the upgrade. Admin accounts and
  accounts with two-factor authentication are not changed this way; for them, and after the 30
  days, an admin has to remove the old account. Each of these changes is written to the audit
  log as `auth.saml_rekey`. Until no account is left, a notice lists the accounts still
  waiting; under Authentication you can end re-keying for all of them early
  (`auth.saml_rekey_end`), which closes the window in which another user of the identity
  provider with the same email could take such an account. With an identity provider on
  another domain, Dockmesh must be reached over HTTPS.
- [sso-linked-accounts-review, admins] Earlier versions linked SSO logins to accounts by email
  alone. A notice lists how many accounts are linked to SSO and which of them are admins; check
  that each belongs to the right person.
- [host-admin-system-settings, admins] Host Admins no longer change system settings, SSO or
  global environment variables. Give the Admin role to anyone who still needs that.
- [backup-job-name-collision, admins] Backup jobs whose names differ only in case or
  punctuation, or a non-system job named like `dockmesh-system`, share an archive folder and
  their retention can delete each other's backups. A notice lists the affected jobs; rename
  one job of each group under Backups. The notice goes away once no names collide.
- [premigrate-snapshot, admins] Make sure the database directory has room for a full copy
  of the database. Before schema changes Dockmesh writes `<db>.pre-migrate-<timestamp>.bak`
  next to the database (`dockmesh.db.pre-migrate-20261029T101500.000Z.bak`, UTC) and keeps
  the newest three; system backups leave these copies out. If the copy cannot be written, Dockmesh does not start and does not change
  the database; set `DOCKMESH_SKIP_PREMIGRATE_BACKUP=1` to upgrade without it. To roll back,
  stop the service, copy the `.bak` file over the database file, delete the database's
  `-wal` and `-shm` files if present, and start the previous version.
- [redeploy-relative-bind-mounts, admins] Redeploy every stack that uses a relative bind mount
  (`./path:/...`). Dockmesh lists the affected stacks in a notice until each one is redeployed.
  On a remote host, upgrade the agent first: an older agent keeps the temporary mount.
- [git-webhook-secret, admins] Git webhooks for stacks without a webhook secret are disabled;
  polling and manual sync keep working. A notice lists the affected stacks. To keep using a
  webhook, edit the stack's git source, set a webhook secret and enter the same secret in the
  webhook settings of your git provider.
- [setup-force-flag, admins] If a notice says the setup flag was ignored, delete the
  `DOCKMESH_SETUP_FORCE=true` line from the env file it names (by default
  `/var/lib/dockmesh/dockmesh.env`) and restart Dockmesh. The notice goes away after the restart.
