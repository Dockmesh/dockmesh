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
- Every audit log entry has a category (`changes`, `access`, `auth` or `system`), and the audit
  API filters by it with `?category=`.

### Changed
- Updating the image of a container that belongs to a stack now rebuilds it from the
  stack's compose.yaml, together with the other replicas of its service, and records the
  new image in the stack's deploy history. Because that applies the stack's current
  compose.yaml, it also needs the `stacks.deploy` permission.
- Only the newest three rollback images per image tag are kept after an update
  (`DOCKMESH_UPDATE_ROLLBACK_KEEP` changes the number).
- Update checks for private images use the credentials saved under Registries, and an
  update that was already announced isn't announced again after a restart.
- The proxy now runs a pinned Caddy version (2.11.7, pinned by digest) instead of whatever `caddy:2` pointed
  to; set `DOCKMESH_PROXY_IMAGE` to use another. The proxy container is recreated once after
  this upgrade and then kept across Dockmesh restarts instead of being replaced on every start.
- Proxy route upstreams must be `host:port` and may not point at the proxy's own admin API,
  and a route's host may not be `localhost`, a loopback address or a bare `*`. Existing routes
  that break these rules stop being served and are listed in a notice for admins.

### Fixed
- Updating a container's image no longer loses its anonymous volumes (`-v /data` or a
  `VOLUME` in the image); the new container gets the old volumes.
- A failed image update no longer leaves the container deleted: the old container is kept
  until the new one runs, and is started again if the new one fails. The previous image stays
  tagged for a rollback.
- Locally built images no longer always show "update available".
- Proxy routes keep working after the Caddy container restarts; before, it came back with no
  routes until Dockmesh pushed them again.
- Proxy routes with TLS set to "none" are now served over plain HTTP instead of getting a
  certificate and an HTTPS redirect anyway.
- Coming back to an idle browser, or having Dockmesh open in several tabs, no longer signs
  you out. Tabs now share one session renewal, and a short network outage while renewing
  keeps you signed in and retries instead of returning you to the sign-in page. Container
  file downloads now renew an expired session like every other request.
- The volume detail page no longer reloads endlessly when the volume does not exist; it
  shows an error instead.
- Leaving a stack page during a migration stops its status polling.
- The host detail page now loads the new host when you switch between hosts.
- Starting and stopping stack operations updates the busy state of every open stack page.
- Switching hosts no longer loads the dashboard, containers, stacks and resources pages
  twice, and a slow response for the previously selected host can no longer overwrite the
  new one. Auto-refresh skips a cycle while the previous one is still loading.
- Leaving a container or stack page while its logs, stats or terminal are connecting no
  longer leaves a socket open in the background.

### Security
- A rollback now only accepts update-history entries of the container it is called for, and
  the update history is limited to containers in the caller's scope. Before, a user scoped to
  one stack could roll back containers of other stacks.
- A proxy route can no longer forward to the proxy's own admin API, which would have let anyone
  reaching the proxy change its configuration. The admin API now also refuses requests from
  browsers on other sites.

## [0.4.2] - 2026-10-30 [recommended]

Fixes the backup target wizard (Continue stayed disabled in 0.4.1), the reverse proxy turning
itself off after a restart, and several problems found while testing backups against real SMB,
SFTP, S3 and WebDAV servers.

### Added

- The target wizard can set S3 TLS and a custom CA certificate (also for WebDAV), an S3 prefix with
  nested folders, and an SFTP private key. The key is stored like passwords and never shown again.
- When an SFTP server presents a new host key, the wizard's test step offers "Accept new host key".
- `dockmesh ca reissue-server --add-san <name-or-ip>` issues a new server certificate for the agent
  connection with extra names or addresses, signed by the same CA, so agents trust a restored
  server on a new address without being enrolled again.
- SMB logins written as `DOMAIN\user` or `user@domain` work; an optional `domain` field overrides them.

### Changed

- Every backup run now reads the archive back from the target and compares size and checksum; a
  mismatch or read error fails the run. This transfers each archive a second time, which counts
  against metered S3 traffic.
- Restoring a volume or stack first checks the downloaded archive against the recorded checksum and
  extracts nothing if it differs. This needs temporary disk space as large as the archive.
- Verify checks the archive as stored (before decryption), which is what the recorded checksum covers.
- The SMB client is now the maintained `cloudsoda/go-smb2` fork, which also fixes a known
  vulnerability in the previous library.

### Fixed

- The target wizard's Continue button never enabled (regression in 0.4.1).
- The reverse proxy stays enabled after a restart: enabling or disabling it is now saved, an enabled
  proxy is brought up on start and its routes are loaded, and the page shows the real state. Installs
  where the proxy was enabled through the button in earlier versions (container running, setting
  never saved) are detected on the first start and saved as enabled. Fixes #58.
- Large archives no longer fail uploading to WebDAV with a client timeout; transfers use dial, TLS,
  header and idle timeouts instead of an overall limit. WebDAV uploads no longer hold the archive in memory.
- Encrypted backups on a ksmbd SMB server restored incomplete data because the final read was
  rejected; the restore now succeeds and a failed read can no longer pass for a complete archive.
- Verify failed on every encrypted backup because it hashed the decrypted data.
- SMB writes no longer fail intermittently with "signing required" when the server requires signing.
- Testing an SFTP target whose base folder does not exist yet now creates it, as a run does.
- Server certificate files that did not match each other (after a failed re-issue) are repaired on
  start instead of silently disabling the agent connection.

### Upgrade notes

- [move-to-new-address, admins] Only needed if you restore Dockmesh onto a server with a different
  address. (1) Install Dockmesh on the new host and run `dockmesh restore --from <backup.tar.gz>`.
  (2) Run `dockmesh ca reissue-server --add-san <new-ip> [--add-san <new-name>]`; skip this when
  agents connect by a name the certificate already covers and you only update DNS. (3) Set
  `DOCKMESH_AGENT_PUBLIC_URL=wss://<new-address>:8443/connect` (and `DOCKMESH_BASE_URL` if it names
  the old address) in `dockmesh.env` and restart Dockmesh. (4) On each agent set
  `DOCKMESH_AGENT_URL=wss://<new-address>:8443/connect` in `/etc/dockmesh-agent/agent.env` and
  restart `dockmesh-agent`; agents keep their certificates, no new token is needed. (5) Check that
  every agent shows online.

## [0.4.1] - 2026-10-30 [security]

Security and maintenance release. It fixes three authorization and sign-in problems in 0.4.0 and
earlier, makes the installers more reliable on upgrade, and repairs volume browsing on agent hosts.

### Security

- GHSA-2g85-893g-rppp (moderate): SSO sign-in put session tokens in the redirect URL
- GHSA-9fwq-xwp6-x857 (high): Template deploy skipped host and role-scope checks
- GHSA-mj64-3wx9-h457 (moderate): Host-scoped roles could read files of undeployed stacks
- Updated the frontend framework (Svelte) to a release that fixes DOM clobbering and
  attribute-handling XSS issues.
- SSO sign-in (OIDC, OAuth2, SAML) no longer puts session tokens in the redirect URL. The
  browser gets a one-time code that expires after a minute and only works in the browser that
  completed the sign-in, so a crafted link can no longer sign someone in to another account.
- Deploying a template now applies the same host and role-scope checks as deploying a stack and
  is recorded in the deploy history. Deploying a stack that pulls in dependency stacks also
  checks the caller's role scope for each dependency.
- Users and roles limited to certain hosts or host tags can no longer read the compose file and
  `.env` of a stack that has not been deployed yet, unless a role lists that stack by name.
- The first-run setup wizard's state-changing calls (`/setup/commit` and the validation
  helpers) now require a header only the wizard sends, so a website opened in the same browser
  during setup cannot start the installation or probe URLs from your network.
- Alert emails no longer let a rule or alert name inject extra headers (such as extra recipients)
  into the message.
- Two simultaneous requests to refresh a session with the same token can no longer both
  succeed; only the first one rotates the session.
- Notifications sent to everyone (alerts, backups, agent offline, deploys, image and version
  updates, migrations) now reach only users whose role has the matching permission and whose
  host and stack scope covers the event, instead of every user. Backup notifications follow the
  job's host, those of system backups reach only unrestricted admins, and alert notifications
  count as events of the server's own host.
- Git webhook secrets are now stored encrypted in the database, like the git password and SSH
  key. Secrets saved by earlier versions are encrypted automatically on the next start and
  existing webhooks keep working without changes.
- The system settings API no longer returns the audit webhook signing secret or URL to users who
  may change system settings but not the audit configuration, and those users can no longer
  rewrite the audit webhook through it. Global environment variables marked secret are no
  longer shown in API responses; leaving the value empty when editing keeps the stored one.
- The short-lived container that reads volumes for the volume browser now uses an Alpine image
  pinned by digest, drops every capability except reading files of other owners, cannot gain
  privileges, has process and memory limits, and its output is capped.

### Fixed

- Backups to an SMB share (for example a Synology NAS) from a Linux server no longer fail with
  "file does not exist": the `jobs/<job name>` folder is now created on the share before the
  archive is written, and a restore from SMB no longer hangs on closing the connection. (#53)
- A backup job created from a saved target in the job wizard no longer fails with a login error
  (for example "The attempted logon is invalid" on SMB) while the target's connection test
  succeeds. Since 0.4.0 the wizard copied the target's hidden, empty password into the job; the
  server now takes the password from the saved target. Jobs created that way on 0.4.0 are repaired
  once, on the first start after the upgrade: when exactly one saved target has the same type, server and location, the job
  takes its password and an info notice lists the repaired jobs. Jobs without a unique match raise a
  notice asking you to enter the password again in the job. (#54)
- `dockmesh init` now writes `DOCKMESH_HTTP_ADDR` to the env file, so the listen address
  you chose takes effect instead of being ignored.
- The Operator role can create its own API tokens again, as the permission list always
  promised, and the permission counts no longer include image, volume and network update
  permissions that do not exist.
- dmctl: `containers list --all`, `--host all` and `alert list-active` now return results,
  `backup run` prints the finished run, `backup runs` shows the archive size, and long backups
  and stack deploys are no longer cut off after 60 seconds. `logs` only keeps streaming with
  `--follow`.
- Several UI texts now match what the code does: permission names on the Registries,
  Authentication, API tokens and Users pages, Drain (no cordon), host tags (scoping, not stack
  placement), global variables (hidden, not "encrypted"), and the stack delete description.
  The invite dialog's link expiry now takes effect (1 to 7 days) and its unused initial
  password field is gone. The Templates page shows each button by the permission its API call
  needs.
- The group-mapping hints now say what the code does (the last matching group decides, and OIDC
  keeps only one admin and one operator group), and the audit webhook placeholder no longer
  shows an internal-looking hostname.
- The notice about SAML accounts waiting for their re-key now disappears once the re-key
  window has passed, without restarting the server.
- Opening an image that was never scanned no longer logs a 404 error in the browser console;
  `GET /images/{id}/scan` now answers 200 with `{"scanned": false}`.
- The agent logs the verified SHA-256 of a downloaded upgrade binary before replacing itself.
- The backup wizards now prefill the data directory's `backups` folder (or the first allowed
  local directory) for local targets instead of `./data/backups`, which the allowed-directory
  check rejected.
- The users list now marks locked accounts and offers an Unlock action, so an admin no longer
  needs the API or `dockmesh admin unlock` to release someone locked out by failed sign-ins.
- The installer now detects an existing Dockmesh service reliably on upgrade, always restarts
  it after replacing the binary and stops with an error if the old binary is still running.
- The agent installer now reports success only once the agent has connected to the server.
  If enrollment fails or the server is unreachable it exits with an error and the last agent
  log lines instead of printing "installed and running".
- New agents keep their data in `/var/lib/dockmesh-agent` instead of `/var/lib/dockmesh`, so an
  agent on the same host as the server no longer mixes its files with the server's. Re-running
  the installer on an existing agent keeps its current directory, unless that is the server's
  data directory: then the agent's own files move to `/var/lib/dockmesh-agent` and the server's
  directory keeps its owner and permissions.
- `uninstall.sh --dry-run` no longer kills the processes of the `dockmesh` service user.
- Linux release tarballs now include the agent for both amd64 and arm64, and the installer
  places both, so a server of one architecture can enroll hosts of the other.
- `install.sh` now refuses to install when the release has no published checksum for the
  download (set `DOCKMESH_SKIP_CHECKSUM=1` to override) instead of continuing with a warning.
- The `dockmesh` command-line tools (`dockmesh admin ...`) also find the settings file in
  `/data/dockmesh.env`, where the installer puts it on hosts with a separate `/data` volume.
  A settings file found this way is only read when it belongs to root, the `dockmesh` service user or the user running
  the command and nobody else can write to it. Otherwise the command says so; run it as that
  user (`sudo -u dockmesh dockmesh admin ...`) or pass `--env-file`.
- Browsing a volume on an agent host works again. The agent now reads the volume through a
  short-lived read-only helper container, like the server does, instead of failing with
  "permission denied" on every folder. File names that start with `-` or contain a newline or
  `|` are listed correctly, and a name with a newline can no longer add fake rows.
- Opening a path that leaves the volume (`..` or a symlink pointing outside) now answers with a
  400 error on every host instead of a server error, and a missing file answers 404.
- Saving changes that first read and then update the database, such as re-enrolling all
  agents or raising a notice, now waits for other writes to finish instead of failing with
  "database is locked" when several things write at the same time.

### Upgrade notes

- [update-agents, admins] Update your agents from the Hosts page. Volume browsing on agent hosts
  only works with the 0.4.1 agent.

## [0.4.0] - 2026-10-29 [security]

Security release. It fixes authorization, sign-in, agent enrollment and backup problems found
in a review of the 0.3 code. Update soon; several changes need an admin's attention afterwards,
see Upgrade notes.

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
- GHSA-22fg-hmg7-9x6w (high): Half-finished two-factor login could turn two-factor authentication off
- GHSA-hxmj-9372-56xx (high): WebSocket connections ignored the caller's role and host scope
- GHSA-h34h-wr2v-w89h (moderate): Scope restrictions were not applied to several read endpoints
- GHSA-m2fv-5hrc-8xjh (high): Users who may edit users, invites or roles could grant themselves any permission
- GHSA-26g2-j8h2-jc69 (critical): A Host Admin could reach all secrets through the system backup
- GHSA-6pcr-2hh4-gwg9 (moderate): Backup archives could be downloaded by every role
- GHSA-62wv-8jrc-cww7 (high): Symlinks could lead volume browsing, restore and Git sources out of their directory
- GHSA-9qxm-6p2m-77ch (high): Backup restore accepted a host path as its target
- GHSA-8mf8-c6qw-xc7j (moderate): Git webhooks could be triggered without authentication
- GHSA-46r3-rjv8-79g8 (high): The setup wizard could be reopened and misused after a reinstall
- GHSA-7r93-wr7q-hv2p (high): Client address headers were trusted from anyone and several sign-in paths had no rate limit
- GHSA-3p82-vprr-2m4p (high): Agents accepted any server during enrollment and did not verify self-upgrades
- GHSA-j4wx-v546-r4fj (high): SFTP backup targets did not verify the server's host key
- GHSA-x6v3-qvjw-56xq (high): Backup hooks could run commands in containers outside the creator's permissions
- GHSA-q2m7-rqw3-3hrp (high): Single sign-on attached logins to existing accounts by email alone
- GHSA-h743-wmjg-j2xx (moderate): API token checks could exhaust the server's memory
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
- The SQLite driver is updated. With the old version, a request that a client cancelled at an
  unlucky moment (a closed browser tab, a dropped connection) could leave a database connection
  failing every later query until the restart.
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
  unverified way. For a fully verified path, re-issue the host's token on its host page and run
  the new install command on the host: the installer checks the binary's SHA-256, pins the
  agent CA, and the host keeps its ID, stacks and backup jobs.
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
  global environment variables, and no longer see or download system backups. Give the Admin
  role to anyone who still needs that.
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
- [stack-files-outside-changes, admins] Dockmesh now remembers the `compose.yaml` and `.env`
  it last wrote for each stack, taking the files as they are at a stack's first deploy or
  check after the upgrade. If you edit stack files on the server by hand, or a container
  changes them, the stack page says "modified outside Dockmesh" and the next deploy asks you
  to confirm the new content. Git syncs of such a stack wait until you confirm.
- [setup-force-flag, admins] If a notice says the setup flag was ignored, delete the
  `DOCKMESH_SETUP_FORCE=true` line from the env file it names (by default
  `/var/lib/dockmesh/dockmesh.env`) and restart Dockmesh. The notice goes away after the restart.
