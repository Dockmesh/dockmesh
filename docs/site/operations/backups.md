---
title: Backups
description: Back up volumes, stacks and the Dockmesh server itself to local disk, S3, SFTP, SMB or WebDAV, and restore them.
---

Dockmesh runs backup jobs on a cron schedule and writes each run as one gzipped tar archive to a target. Archives can be encrypted with the server's age key. Everything lives under **Backups**, which has three tabs: **Jobs**, **Runs** and **Targets**.

Dockmesh does not back up anything by default. Create at least one job for the server itself (see [Disaster recovery](./disaster-recovery.md)) and one per stack or volume that holds data you care about.

## Sources

Each job has exactly one source. To back up three stacks, create three jobs.

| Source | Wizard label | What goes into the archive |
|---|---|---|
| `system` | **Whole dockmesh** | A consistent copy of the SQLite database (`VACUUM INTO`), the stacks directory and the data directory (keys, JWT secret, PKI, audit genesis). No Docker volumes. |
| `stack` | **Single stack** | Every file in `stacks/<name>/` plus every named volume declared in the stack's `compose.yaml`, each as a nested tar. |
| `volume` | **Single volume** | The contents of one Docker volume. |

What a stack backup does not contain: bind-mounted host paths and anything outside the stack directory. Containers are not stopped while the archive is written, so a database that is busy writing can end up inconsistent. Use a [hook](#hooks) to dump it first.

<!-- update after: backups: keep retention per job, fail on partial or missing data -->
If the stack's compose file cannot be parsed, or a volume cannot be read, the run still reports success and the archive is missing that data. The run log does not mention skipped data, so after the first run of every new job download the archive and check that it contains `volumes/<name>.tar.gz` for every volume you expect.

Every job runs on one host (`host_id`, empty means the server host):

- A **volume** job runs on the host you pick; the wizard then lists that host's volumes. Changing the host of an existing job asks for the volume again.
- A **stack** job runs on the host the stack is deployed on. The wizard shows that host and doesn't let you change it. A stack that isn't deployed can't be backed up. Through the API, a stack job sent without `host_id` gets its deployment host.
- A **system** job always runs on the server host.

The wizard only offers hosts on which you may create (or, when editing, change) backup jobs. When a job is saved with a new host or source, the server checks that the host exists, that the volume exists on it, and that the stack is deployed there, and rejects the job otherwise.

Remote volumes stream through the server. The agent stream can drop chunks under load, so archives of remote volumes may be silently incomplete; test-restore them. Volume backups use a small helper container (`alpine:3.24`), which Dockmesh pulls if it is missing.

## Targets

You can save targets under **Targets** → **New target** and pick one in the job wizard, or configure a target inline in the wizard.

<!-- update after: backups: run in the background, set the time zone, use saved targets -->
A job stores its own copy of the target configuration, including credentials. Editing a saved target later does not change jobs that were created from it, and a saved target can be deleted while jobs still use its settings.

| Type | Config fields | Defaults |
|---|---|---|
| Local | `path` | `<data directory>/backups` (see below) |
| S3 | `endpoint`, `bucket`, `region`, `access_key`, `secret_key`, `prefix`, `use_ssl` | region `us-east-1` |
| SFTP | `host`, `port`, `username`, `password`, `private_key`, `path` | port 22, path `/backups` |
| SMB | `host`, `port`, `share`, `username`, `password`, `path` | port 445, path `backups` |
| WebDAV | `url`, `username`, `password`, `path` | path `/backups` |

Archives are written to `jobs/<job-name>/<UTC timestamp>-r<run id>.tar.gz`, with `.age` appended when the run is encrypted. The job name is lowercased and every character other than `a-z`, `0-9` and `-` becomes `-`.

<!-- update after: backups: scope hooks, limit targets, check sftp keys; fix bind mounts -->
<!-- update after: backups: run in the background, set the time zone, use saved targets -->
Know the limits of each target type before you rely on it:

- **Local**: a relative path is resolved against the server's working directory, which is `/` under the systemd unit that `install.sh` writes. A local target without a path writes to `<data directory>/backups`, or to `./data/backups` below the working directory if that already held archives when the server first started on this version; the choice is saved in the `backups.empty_path_dir` setting and doesn't change later. Always enter an absolute path, such as `/var/lib/dockmesh-backups`, and keep it outside the data directory. Otherwise every system backup also contains all earlier backups. A local target on the server's own disk does not protect you against losing that disk.
- **S3**: `use_ssl` defaults to false, so a target talks plain HTTP to the endpoint unless **Use TLS** is ticked in the target or job form. Each archive is first written to a temporary file on the server and then uploaded in one request, so the server needs free temporary disk space as large as the archive.
- **SFTP**: the target and job forms take a password, a private key (PEM or OpenSSH) or both.
- **SMB**: does not work when the Dockmesh server runs on Linux. The job folder is never created and listing returns wrong paths, so retention deletes nothing. Do not use SMB targets there.
- **WebDAV**: the whole archive is held in server memory before upload, and requests time out after 30 seconds. **Test connection** always reports success for WebDAV. Use it only for small archives.
- **Inline port**: the inline form in the job wizard has no SMB port field. For a non-default SMB port, create a saved target and pick it.

## Creating a job

Click **New backup job** on the **Jobs** tab. The wizard asks for:

1. **Job name** and **Source**: pick a stack, a host and one of its volumes, or **Whole dockmesh**.
2. **Frequency**: hourly, every 6 hours, daily, weekly, or a raw 5-field cron expression.
3. **Write to target**: a saved target or an inline one.
4. Encryption, retention and pre- and post-backup hooks.

<!-- update after: backups: run in the background, set the time zone, use saved targets -->
Schedules use the server's local time zone (the time zone of the `dockmesh` process). A new run starts even if the previous run of the same job is still going, so pick intervals that leave enough room.

### Retention

Retention runs after each run and has two settings, **Keep the newest N runs** (`retention_count`) and **Max age (days)** (`retention_days`). 0 turns a rule off. Both rules are applied independently: an archive is deleted if it is not among the newest N, or if it is older than the maximum age, even when it is among the newest N. The newest archive is never deleted.

<!-- update after: backups: keep retention per job, fail on partial or missing data -->
Retention works on files in the target, not on the run history. It deletes every file under `jobs/<job-name>` that matches the rules, including archives from failed runs. On S3 the match is a plain prefix, so the retention of a job named `db` also deletes archives of `db-prod`. Two jobs whose names normalise to the same folder share one retention pool. Give every job a distinct name that is not a prefix of another job's name.

### Encryption

With **Encrypt at rest with age** on, the archive is encrypted to the server's age key (`secrets.age-key` in the data directory), the same key that protects stack `.env` files. There is no per-job key or passphrase.

Encryption only happens when secrets encryption is enabled (`DOCKMESH_SECRETS_ENCRYPT_ENV`, on by default). With it off, the archive is written unencrypted but still gets the `.age` suffix and is listed as encrypted.

Keep a copy of the key outside the server: without it nobody can decrypt the archives, Dockmesh included. How to export it is covered in [Disaster recovery](./disaster-recovery.md#export-the-age-key). If you rotate the key (**Settings** → **Rotate key**), Dockmesh can no longer decrypt archives made with the old key. Only one previous key is kept, as `secrets.age-key.old`, so export the key again after every rotation and keep the old copies.

## Hooks

<!-- update after: backups: scope hooks, limit targets, check sftp keys; fix bind mounts -->
A hook is a command that Dockmesh runs with `docker exec` in a named container on the job's host. Pre-hooks run before the archive is written, post-hooks after it. Each hook has a 5-minute timeout, and up to 16 KB of its output goes into the run log. A failing pre-hook fails the run before anything is written. A failing post-hook marks the run as a success with warnings: the archive is complete and can be restored.

The last step of the job wizard lists the pre- and post-backup hooks as rows of container and command. The container field suggests the containers of the picked stack, or those that mount the picked volume. The wizard runs each command with `sh -c`, so redirects and the container's environment variables work. A hook saved through the API keeps its exact `cmd` as long as its row isn't edited.

The presets (**PostgreSQL dump**, **MySQL dump**, **MariaDB dump**, **Redis save**, **MongoDB dump**) add a row with a command for the official images. They write the dump into the image's data directory (for example `/var/lib/mysql/dockmesh-dump.sql`), which is the volume a stack backup includes. Pick the container, and change the path if your container keeps its data somewhere else. A dedicated volume such as `dumps:/dumps` in the service keeps the dump apart from the live data directory.

Through the API, `cmd` is an argv list. Use `["sh", "-c", "..."]` when the command needs a shell:

```bash
DM=https://dockmesh.example.com/api/v1
TOKEN=dmt_xxxxxxxx
JOB=7
curl -s -H "Authorization: Bearer $TOKEN" "$DM/backups/jobs/$JOB" \
| jq '{name, host_id, target_type, target_config, sources, schedule,
       retention_count, retention_days, encrypt, enabled, post_hooks,
       pre_hooks: [{container: "analytics-db-1",
                    cmd: ["sh", "-c", "pg_dumpall -U postgres > /dumps/all.sql"]}]}' \
| curl -s -X PUT -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
       --data @- "$DM/backups/jobs/$JOB"
```

## Default system backup

`PUT /api/v1/backups/system/enabled` with `{"enabled": true}` creates a job named `dockmesh-system`: system source, daily at `0 3 * * *`, 14 days retention, local target `<data directory>/backups`, encrypted when secrets encryption is on. If a job with a system source already exists, no new job is created. `{"enabled": false}` disables it. The UI has no switch for this; you can also create the same job in the wizard with **Whole dockmesh**. Change its target to an absolute path, or better an off-host target, for the reasons given under [Targets](#targets).

Installs upgraded from older versions may still have an auto-created `dockmesh-system` job. The **Jobs** tab flags it for review with **Keep** and **Disable** buttons.

## Running and monitoring

**Run now** (on a job card, in the job drawer and on the job page) starts a run immediately and requires `backups.update`.

The run starts in the background, so closing the page does not stop it. Its steps and live log are in the progress dialog (the running-operations indicator in the top bar opens it from any page).

**Cancel** in the progress dialog, in the job and run lists and on the run's page stops a run in any step. It needs the same permission as starting it. A hook that is running finishes first, since Docker cannot stop a command inside a container. Post-backup hooks still run, so a service a pre-backup hook stopped comes back, and the unfinished archive is deleted from the target (if that fails, the next run of the job retries). The run is recorded as failed with "cancelled by a user", and its operation as cancelled, naming the step it stopped in. A cancel during the post-backup hooks comes too late: the archive is already complete and the run succeeds.

A run whose transfer stops moving data, because the target accepts the connection but no longer answers or the source host stops sending, fails after 10 minutes without progress with "target stalled", and the job can run again. Slow transfers that keep moving are never cut off.

The **Runs** tab lists every run with status, size and duration; open one to see its log. Each finished run also posts a success or failure entry to the notification bell. A run that was interrupted by a server restart is marked failed when the server starts again.

## Restore

Click **Restore** on a successful run (in the **Runs** tab or on the run's page) and type the target name to confirm.

Restores go to the host the backup was taken on unless you pick another one. **Merge** writes the archive's files over what is there and keeps everything else. **Replace** makes the volume match the backup: it extracts the archive into a staging directory inside the volume and swaps the old contents out only after it extracted completely, so a broken connection to the agent, a failing extraction or too little free space leaves the volume as it was. Replace needs free space for the old and the new contents side by side, an agent of this version on agent hosts, and no running container using the volume. Dockmesh does not stop containers for you: stop the stack first and redeploy it afterwards.

A restore can be cancelled while it downloads or verifies the archive; nothing has been written then. Once it writes, it runs to the end.

- **Volume run**: restored into the volume you name. The volume is created if it does not exist.
- **Stack run**: the stack files are written to `stacks/<name>/` and the volumes go to the backup's host. Restoring under another stack name gives the copy its own volumes (`<new-stack>_<volume>`); volumes with a fixed name are left alone and listed in the result. It needs permission to edit (or create) that stack. The stack is not redeployed.
- **System run**: **Restore the whole system** (admins only) unpacks the backup next to the live files and checks it, then restarts Dockmesh, which swaps the restored files in before it opens the database. The replaced files are kept in `<data>/.pre-restore/<time>` and `<stacks>.pre-restore-<time>`. **Restore system from file** on the **Runs** tab does the same with an uploaded archive. See [Disaster recovery](./disaster-recovery.md).

The run page also offers **Download archive**, which streams the archive decrypted, as plain `.tar.gz`.

## Verify

Verification has no UI. Both endpoints require `backups.restore`:

| Endpoint | What it checks |
|---|---|
| `POST /api/v1/backups/runs/{id}/verify` | System runs: extracts to a temp directory, opens and migrates the database, checks for an admin user and warns on a missing CA certificate or key, audit genesis or `secrets.env`. Stack and volume runs: walks the archive and compares its checksum. |
| `POST /api/v1/restore/verify` | An uploaded, unencrypted system archive (multipart field `file`), with the same checks as a system run. |

<!-- update after: backups: keep retention per job, fail on partial or missing data -->
Verification of encrypted stack and volume runs always fails, because the stored checksum is that of the encrypted file. For system runs the check does not include the age key itself.

## Permissions

| Backups level or extra | Allows |
|---|---|
| View | List jobs, targets and runs and read run logs, for the hosts and stacks the assignment covers |
| Operate | Run jobs now, restore and verify runs |
| Manage | Create, edit and delete jobs and targets |
| Extra **Download backups** | Download decrypted archives of covered hosts and stacks; stack archives contain the stack's secrets |
| Extra **Export the backup key** | Export the key that decrypts every backup; needs an assignment on everything |

System backups, which contain the database, the JWT secret and the backup key, are admin-only (Settings & audit: Manage on everything). Hooks run commands inside containers, so jobs with hooks also need the Terminal extra on those containers. See [Permissions](../concepts/permissions.md).
