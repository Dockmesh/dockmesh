---
title: Backups and restore
description: Back up volumes, stacks and the Dockmesh server itself to local disk, S3, SFTP, SMB or WebDAV, and restore them.
---

Dockmesh runs backup jobs on a cron schedule and writes each run as one gzipped tar archive to a backup target. Archives can be encrypted with the server's age key. Everything lives under **Backups** in the sidebar, which has three tabs: **Jobs**, **Runs** and **Targets**.

Dockmesh backs up nothing by default. Create at least one job for the server itself (see [Disaster recovery](./disaster-recovery.md)) and one per stack or volume that holds data you care about.

To keep archives off the server, write them to an S3 bucket or another remote target (see [Targets](#targets)). To get data back from a run, see [Restore](#restore).

## Sources

Each job has exactly one source. To back up three stacks, create three jobs.

| Source | Wizard label | What goes into the archive |
|---|---|---|
| `system` | **Whole dockmesh** | A consistent copy of the SQLite database (`VACUUM INTO`), the stacks directory and the data directory (keys, JWT secret, PKI, audit genesis). Left out: `<data>/backups`, leftovers of an earlier system restore and the database snapshots taken before migrations. No Docker volumes. |
| `stack` | **Single stack** | Every file in the stack directory on the server plus every volume declared under the top-level `volumes:` of the stack's `compose.yaml`, each as a nested tar. |
| `volume` | **Single volume** | The contents of one Docker volume. |

A stack backup contains bind-mounted host paths only when they are inside the stack directory on the server. Relative bind mounts (`./data:/data`) of a stack on the server's own host resolve there, so their data is in the archive; on an agent host they resolve to the agent's copy of the stack directory, which is not backed up. Containers are not stopped while the archive is written, so a database that is busy writing can end up inconsistent. Use a [hook](#hooks) to dump it first.

A stack run fails when the compose file can't be parsed or one of its volumes can't be archived. Verification and restore check that every volume the stack had is in the archive.

### Which host a job runs on

Every job runs on one host. The wizard picks it from the source:

- A **volume** job runs on the host you pick; the wizard then lists that host's volumes. Changing the host of an existing job asks for the volume again.
- A **stack** job runs on the host the stack is deployed on. The wizard shows that host and doesn't let you change it. A stack that isn't deployed can't be backed up. Through the API, a stack job sent without `host_id` gets its deployment host.
- A **system** job always runs on the server host.

The wizard only offers hosts on which you may create (or, when editing, change) backup jobs. When a job is saved with a new host or source, the server checks that the host exists, that the volume exists on it, and that the stack is deployed there, and rejects the job otherwise.

Volume backups use a short-lived helper container: `alpine:3.24` on the server's host, `busybox:latest` on agent hosts. Dockmesh pulls the image if it is missing. Archives of agent hosts stream through the server to the target.

## Targets

A target is where archives go. Targets are saved objects: create them under **Backups** → **Targets** → **New target**, or set one up in the job wizard, which saves it under the job's name. Several jobs can share one target. Editing a target changes where every job that uses it writes, the target page lists those jobs under **Jobs writing here**, and a target that jobs still use can't be deleted.

| Type | Wizard label | Config fields | Defaults |
|---|---|---|---|
| `local` | **Local directory** | `path` | see below |
| `s3` | **S3 / object store** | `endpoint`, `bucket`, `region`, `access_key`, `secret_key`, `prefix`, `use_ssl`, `ca_cert` | region `us-east-1`, `use_ssl` off; turn it on for anything but a local MinIO, otherwise credentials and data travel unencrypted |
| `sftp` | **SFTP over SSH** | `host`, `port`, `username`, `password`, `private_key`, `path` | port 22, path `/backups` |
| `smb` | **SMB / NAS share** | `host`, `port`, `share`, `username`, `domain`, `password`, `path` | port 445, path `backups` |
| `webdav` | **WebDAV** | `url`, `username`, `password`, `path`, `ca_cert` | path `/backups` |

The target wizard has three steps (**Type**, **Connection**, **Test**). **Run test** connects, authenticates, writes, reads back and deletes a probe file. You can also save without testing; the next run reports any error.

Passwords, S3 secret keys and SFTP private keys are stored encrypted with the server's secrets key and are never shown again after saving. Leave a secret field empty when editing to keep the stored value.

Archives are written to `jobs/<job id>-<job name>/<UTC timestamp>-r<run id>.tar.gz`, with `.age` appended when the run is encrypted. The job name is lowercased and every character other than `a-z`, `0-9` and `-` becomes `-`. Archives that older versions wrote to `jobs/<job name>/` stay where they are and are still restored and pruned from there.

### Local, S3, SFTP, SMB and WebDAV targets

What to know about each type:

- **Local** writes on the server's disk, also for jobs of agent hosts. A local target may only write below an allowed directory, set in the `backups.local_roots` setting (one absolute path per line). A new install allows `/srv/backups` and `<data directory>/backups`; an upgraded install also allows the paths its local targets already used, and admins get a notice to review them. Changing `backups.local_roots` needs the permission to install Dockmesh updates (Settings & audit: Manage on everything). The wizard suggests `<data directory>/backups`. A local target without a path writes to `<data directory>/backups`, or, on installs upgraded from a version that resolved an empty path against the working directory and already had archives there, to that old `./data/backups` directory; the choice is stored in `backups.empty_path_dir`. A local target on the server's own disk does not protect you against losing that disk.
- **S3**: works with AWS S3 and S3-compatible stores. **Use TLS** is off by default, so the target talks plain HTTP to the endpoint unless you tick it. Archives stream to the bucket without a temporary file.
- **SFTP** takes a password, a private key (PEM or OpenSSH) or both. The server's host key is recorded on the first connection. If it changes later, runs to that target fail, admins get a notice, and the job and target menus offer **Accept new host key…**. Accept it only if you know why the key changed.
- **SMB** connects directly from the server process; nothing is mounted on the host.
- **WebDAV** writes each archive to a temporary file on the server and uploads it when the run finishes, so the server needs free temporary disk space as large as the archive.

A run whose transfer stops moving data, because the target no longer answers or the source host stops sending, fails after 10 minutes without progress with "target stalled", and the job can run again. Slow transfers that keep moving are not cut off.

### Setting `backups.local_roots` and `backups.timezone`

Neither setting has a field in the UI. Set them through `PUT /api/v1/settings`, which takes a list of key/value pairs:

```bash
curl -s -X PUT -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d '[{"key":"backups.local_roots","value":"/srv/backups\n/mnt/nas/dockmesh"},
       {"key":"backups.timezone","value":"Europe/Berlin"}]' \
  https://dockmesh.example.com/api/v1/settings
```

The endpoint needs Settings & audit: Manage. A time zone that is not a valid IANA name is rejected.

## Creating a job

Click **New backup job** on the **Jobs** tab. The wizard has four steps:

1. **Source**: **Job name** and what to back up, with the host as described above.
2. **Schedule**: **Frequency** is hourly, every 6 hours, daily, weekly, or a raw 5-field cron expression. The default is daily at 03:00.
3. **Target**: **Write to target** picks a saved target or sets up a new one. The same step holds the encryption switch (**Encrypt at rest with age**, on by default).
4. **Retention**: how many archives to keep, and the pre- and post-backup hooks.

Job names must be unique after the normalisation above; `dockmesh-system` is reserved for jobs with a system source.

### Schedules and time zone

Schedules run in the time zone set in `backups.timezone`, an IANA name such as `Europe/Berlin`. Without it they use the time zone of the `dockmesh` process. The schedule step shows the zone in use. Changing the setting takes effect at once; runs in progress continue.

A job that is still running is not started a second time: a scheduled run is skipped with a notification, and **Run now** answers that the job is already running.

### Retention

Retention runs after each successful run and has two settings, **Keep the newest N runs** (`retention_count`) and **Max age (days)** (`retention_days`). 0 turns a rule off. Both rules are applied independently: an archive is deleted if it is not among the newest N, or if it is older than the maximum age, even when it is among the newest N. The newest archive is never deleted.

Retention works from the job's own run records, not from a listing of the target, so it never touches files of other jobs. Only successful runs count. A failed run deletes its partial archive right away; if that fails, the next retention pass retries. Runs whose archive retention removed stay in the list as pruned and can no longer be restored. Deleting a job keeps its runs and archives.

### Encryption

With **Encrypt at rest with age** on, the archive is encrypted to the server's age key (`secrets.age-key` in the data directory), the same key that protects stack `.env` files and stored credentials. There is no per-job key or passphrase.

Encryption needs secrets encryption on the server (`DOCKMESH_SECRETS_ENCRYPT_ENV`, on by default). With it off, a job set to encrypt can't be saved or enabled, and an existing one fails its runs instead of writing an unencrypted archive; admins get a notice listing those jobs.

Keep a copy of the key outside the server: without it nobody can decrypt the archives, Dockmesh included. **Download key backup** under **Settings** → **Encryption key** saves it; see [Disaster recovery](./disaster-recovery.md#export-the-age-key). **Rotate key…** on the same page creates a new key. Earlier keys are kept in `secrets.age-key.history` and included in the downloaded key file, so archives from before a rotation can still be restored. Download the key again after every rotation.

## Hooks

A hook is a command that Dockmesh runs with `docker exec` in a named container on the job's host. Pre-backup hooks run before the archive is written, post-backup hooks after it.

| Rule | Behaviour |
|---|---|
| Timeout | 5 minutes per hook |
| Output | Up to 16 KB per hook goes into the run log |
| Pre-hook fails | The run fails before anything is written |
| Post-hook fails | The run is "completed with warnings"; the archive is complete and can be restored |
| Post-hooks after a failure | They run when at least one pre-hook ran, also if a later pre-hook, the archive or the upload failed, so a service a pre-hook stopped comes back |
| Container | Must exist on the job's host when the job is saved |

The last step of the wizard lists the hooks as rows of container and command. The container field suggests the containers of the picked stack, or those that mount the picked volume. The wizard runs each command with `sh -c`, so redirects and the container's environment variables work. A hook saved through the API keeps its exact `cmd` as long as its row isn't edited.

The presets (**PostgreSQL dump**, **MySQL dump**, **MariaDB dump**, **Redis save**, **MongoDB dump**) add a row with a command for the official images. The dump presets write into the image's data directory (for example `/var/lib/mysql/dockmesh-dump.sql`), which is the volume a stack backup includes. Pick the container, and change the path if your container keeps its data elsewhere.

Hooks run commands inside containers, so saving a job with hooks needs the Terminal extra (**Open a terminal in containers**) on each hook container's host or stack. Hooks into Dockmesh's own containers (the server, the reverse proxy, agents and helpers) can only be saved by an admin.

Through the API, `cmd` is an argv list. Use `["sh", "-c", "..."]` when the command needs a shell:

```bash
DM=https://dockmesh.example.com/api/v1
TOKEN=dmt_xxxxxxxx
JOB=7
curl -s -H "Authorization: Bearer $TOKEN" "$DM/backups/jobs/$JOB" \
| jq '{name, host_id, target_id, sources, schedule,
       retention_count, retention_days, encrypt, enabled, post_hooks,
       pre_hooks: [{container: "analytics-db-1",
                    cmd: ["sh", "-c", "pg_dumpall -U postgres > /var/lib/postgresql/data/all.sql"]}]}' \
| curl -s -X PUT -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
       --data @- "$DM/backups/jobs/$JOB"
```

## Default system backup

`PUT /api/v1/backups/system/enabled` with `{"enabled": true}` creates a job named `dockmesh-system`: system source, daily at `0 3 * * *`, 14 days retention, local target `<data directory>/backups`, encrypted when secrets encryption is on. If a job with a system source already exists, it is enabled instead. `{"enabled": false}` disables it. The endpoint is admin-only and the UI has no switch for it; you can create the same job in the wizard with **Whole dockmesh**. Point it at an off-host target, for the reasons given under [Targets](#targets).

Installs upgraded from versions that created this job on their own flag it for review on the **Jobs** tab with **Keep** and **Disable** buttons.

## Running and monitoring

**Run now** (in a job's row menu and on the job page) and **Run all enabled** in the page header start runs in the background, so closing the page does not stop them. Each run shows its steps and live log in the progress dialog; the running-operations indicator in the top bar opens it from any page. The steps are: connect to host and target, pre-backup hooks, archive and upload, read back and verify, post-backup hooks, retention. After the upload the archive is read back from the target and its SHA-256 compared with what was written.

The **Runs** tab lists every run with its status, size and duration; open one for its log and details. A failed run, or one that completed with warnings, sends a notification. Successful runs don't; they are listed under **Activity**. A run that was still running when the server stopped is marked failed at the next start.

From the command line, `dmctl backup run <job>` starts a run, waits until it finishes and exits non-zero when it failed; `--no-wait` returns once it started. See [dmctl](../reference/dmctl.md).

The server exports `dockmesh_backup_last_run_timestamp_seconds` and `dockmesh_backup_last_duration_seconds` per job (label `job`) for the most recent successful run; see [Monitoring](../features/monitoring.md#prometheus-metrics). The built-in alert rules for failed and overdue backups are described in [Alerts](../features/alerts.md).

### Cancelling a run

**Cancel** in the progress dialog and in the job and run lists stops a run in any step. It needs the same permission as starting it. A hook that is running finishes first, since Docker can't stop a command inside a container. Post-backup hooks still run, and the unfinished archive is deleted from the target (if that fails, the next run of the job retries). The run is recorded as failed with "cancelled by a user", and its operation as cancelled, naming the step it stopped in. A cancel during the post-backup hooks comes too late: the archive is already complete and the run succeeds.

## Restore

Click **Restore…** in a run's row menu or on the run's page, or **Restore last run…** in a job's menu. The dialog asks for:

- **Volume name** or **Stack name**: where to restore. It defaults to the source; a volume is created if it doesn't exist.
- **Host**: defaults to the host the backup was taken on, marked "(backed up here)". You can pick another host you may restore on.
- **Existing data**: **Replace** (the default) or **Merge**.
- The target name, typed to confirm.

**Merge** writes the archive's files over what is there and keeps everything else. **Replace** makes the volume match the backup: it extracts the archive into a staging directory inside the volume and swaps the old contents out only after it extracted completely, so a broken connection to the agent, a failing extraction or too little free space leaves the volume as it was. Replace needs free space for the old and new contents side by side, a run with a recorded checksum (older runs can only be merged), an up-to-date agent on agent hosts, and no running container using the volume. Dockmesh does not stop containers for you: stop the stack first and deploy it afterwards.

Every restore downloads the archive once, checks it against the recorded checksum, verifies it completely and restores from that same copy, so a damaged or swapped archive never overwrites anything. Only one restore at a time may write a given volume or stack, and at most two restores run at once. A restore shows its steps in the progress dialog and can be cancelled while it downloads or verifies the archive; nothing has been written then. Once it writes, it runs to the end.

- **Volume run**: restored into the named volume on the chosen host.
- **Stack run**: the stack files are written to the stack directory on the server and the volumes go to the chosen host. Restoring under another stack name gives the copy its own volumes (`<new-stack>_<volume>`); volumes with a fixed `name:` are left alone and listed as warnings. The stack is not redeployed; deploy it afterwards. The stack page of a stack whose `compose.yaml` is missing offers **Restore from last backup**, which restores its newest stack run in place with **Replace**.
- **System run**: see [Restoring the whole system](#restoring-the-whole-system).

Restoring into one of Dockmesh's own volumes (the proxy and agent volumes, or any volume a server, proxy or agent container mounts) is refused for non-admins.

The run page also offers **Download archive**, which streams the archive decrypted, as plain `.tar.gz`.

### Restoring the whole system

**Restore system…** on a system run, or **Restore system from file…** on the **Runs** tab for an uploaded `.tar.gz` or `.tar.gz.age`, opens **Restore the whole system**. For an encrypted archive of another install, add the backup key file exported there. **Check backup** unpacks the archive next to the live files and runs the restore checks; nothing is replaced yet. **Restart and restore** then restarts Dockmesh, which swaps the restored files in before it opens the database. Volumes and running containers are not touched. Sign in again with an account of the restored install.

The replaced files are kept in `<data>/.pre-restore/<time>` and `<stacks>.pre-restore-<time>`. They include the old database and secret key, so the **Backups** page shows them to admins with a **Delete** button; remove them once the restored install works.

System restores need Settings & audit: Manage on everything. Restoring onto a fresh install and from the command line is covered in [Disaster recovery](./disaster-recovery.md#rebuild-a-server).

## Verify

Verification of a single run has no UI. Both endpoints need the **Operate** level for backups (`backups.restore`):

| Endpoint | What it checks |
|---|---|
| `POST /api/v1/backups/runs/{id}/verify` | Stack and volume runs: walks the archive (decrypting encrypted runs), checks that every volume of a stack is present, and compares the SHA-256 of the stored file with the recorded one. System runs (admins only): extracts to a temporary directory, opens and migrates the database, checks for an admin user and warns on a missing CA certificate or key, audit genesis or `secrets.env`. |
| `POST /api/v1/restore/verify` | An uploaded, unencrypted system archive (multipart field `file`), with the same checks as a system run. |

## Permissions

Backup permissions come from the **Backups** area of a role, on assignments that cover the job's host (everything, a host or a host tag). An assignment on a single stack does not reach backup jobs.

| Backups level or extra | Allows |
|---|---|
| View | List jobs, targets and runs and read run logs on covered hosts |
| Operate | Run jobs now, restore and verify runs |
| Manage | Create, edit and delete jobs and targets |
| Extra **Download backup archives** | Download decrypted archives of covered hosts; stack archives contain the stack's secrets |
| Extra **Export the backup encryption key** | Download the key that decrypts every backup; needs an assignment on everything |

Further rules:

- The host of a run is the one it was taken on, even if its job was moved later; viewing and restoring a run, and where a restore goes by default, follow that host. Restoring onto another host needs that host in scope too.
- Restoring a stack run writes the stack's compose file and environment, so it also needs **Deploy** on that stack (**Manage** to create a new one), on the restore host and on the host the stack is deployed on.
- Testing, editing or deleting a saved target needs access to every job that uses it.
- System backup jobs, their runs and their targets are admin-only (Settings & audit: Manage on everything), as is the target the audit log is archived to. Jobs and runs that back up Dockmesh's own volumes are admin-only too, since those volumes hold the proxy's TLS keys.

See [Permissions](../concepts/permissions.md).
