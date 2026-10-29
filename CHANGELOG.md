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
- Each release now ships a `release-manifest.json` with its severity, advisories and
  required upgrade steps, and its release notes are taken from this changelog.
- When something needs your attention after an upgrade, Dockmesh now shows it as a banner
  and at the top of the notifications drawer, with a link to where you fix it. The notice
  goes away on its own once the issue is fixed; non-critical ones can be hidden from the
  banner.
- Backup schedules run in the time zone set in `backups.timezone` (an IANA name such as
  `Europe/Berlin`); without it they use the server's time zone, which the job wizard now
  shows next to the schedule.

### Changed
- Backup jobs now write to saved backup targets. On upgrade, the target settings stored in
  each job are moved to a saved target (jobs with identical settings share one), and a new
  target set up in the job wizard is saved under the job's name. A target that jobs still use
  can't be deleted.
- "Run now" starts the backup in the background and returns at once; closing the browser no
  longer cancels it. A job that is still running is not started a second time, and a
  scheduled run is skipped (with a notification) while the previous one is in progress.
- S3 and WebDAV backups stream to the target instead of being held in a temporary file or in
  memory first.
- Migrating a stack that uses named volumes to another host is refused with an explanation,
  including in the pre-flight checks. Volume data isn't copied yet, so such a migration used
  to finish with empty volumes on the new host.
- Rotating the secrets key keeps every previous key in `secrets.age-key.history`, and the
  exported backup key file contains them all, so backups made before a rotation can still be
  restored. An interrupted rotation no longer leaves the server without a usable key.

### Security
- Volume browsing, system restore and Git-synced stacks no longer follow symlinks out of the
  volume, the data directory or the repository. Before, a symlink in a volume or repository,
  or a crafted backup archive, could read or overwrite files on the host.
- A Git source's `path_in_repo` must stay inside the repository.
- Restoring a volume backup only accepts a Docker volume name as the target; a path such as
  `/etc` was mounted from the host and overwritten.
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
  rollback asks for confirmation.
- A backup job set to encrypt no longer writes an unencrypted archive when secrets encryption
  is turned off; the run fails and admins get a notice.

### Fixed
- Relative bind mounts such as `./html:/usr/share/nginx/html` now point at the stack's own
  directory. They used to point at a temporary directory that was deleted after the deploy, so
  the container saw an empty folder and whatever it wrote there was lost. On the first redeploy
  of an affected stack, files the old container wrote are copied into the stack directory if
  it is still empty. On remote hosts the directory starts empty, because files next to
  `compose.yaml` aren't copied to agents yet. A bind of the whole stack directory (`.:/app`)
  mounts it read-write like docker compose does, and the stack page warns that the container
  can change `compose.yaml` and `.env`.
- Backup retention only deletes archives of the job it belongs to. Jobs whose names looked
  alike (`DB Prod` and `db-prod`) shared a folder, and on S3 a job `db` also matched
  `db-prod`, so one job's retention could delete another job's backups. New archives go to a
  folder named after the job ID; existing archives are still managed where they are. The
  newest archive of a job is never removed by retention.
- SMB backup targets work again when Dockmesh runs on Linux: the job's folder on the share was
  never created, so every run failed with "file does not exist".
- A failed backup run deletes its partial archive and is no longer counted by retention, so
  a few failed runs can't push the last good backups out.
- A stack backup fails when the compose file can't be read or one of the stack's volumes
  can't be archived, instead of reporting success without that volume. Volume archives from
  remote hosts fail when `tar` exits with an error (needs the updated agent).
- Backup verification works for encrypted backups (it reported a hash mismatch for every
  one), checks the whole file, and for stack backups checks that every volume the stack had
  is in the archive.
- A failing post-hook marks the run "completed with warnings" instead of failed; the archive
  can still be restored. Post-hooks also run when a later pre-hook or the upload fails, so a
  service stopped by a pre-hook is started again.
- Deleting a backup job keeps its runs restorable; runs whose archive was removed by
  retention are shown as pruned.
- Backup runs that were still running when the server stopped are marked failed at the
  next start instead of showing "running" forever.

### Upgrade notes
- [redeploy-relative-bind-mounts, admins] Redeploy every stack that uses a relative bind mount
  (`./path:/...`). Dockmesh lists the affected stacks in a notice until each one is redeployed.
