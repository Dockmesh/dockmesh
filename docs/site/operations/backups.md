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

Jobs created in the web UI always read volumes from the local host; the wizard has no host picker. A `volume` job for a remote host can be created through the API by setting `host_id`, and the data then streams through the server. The agent stream can drop chunks under load, so archives of remote volumes may be silently incomplete; test-restore them. Volume backups use a small helper container (`alpine:3.19`), which Dockmesh pulls if it is missing.

## Targets

You can save targets under **Targets** → **New target** and pick one in the job wizard, or configure a target inline in the wizard.

<!-- update after: backups: run in the background, set the time zone, use saved targets -->
A job stores its own copy of the target configuration, including credentials. Editing a saved target later does not change jobs that were created from it, and a saved target can be deleted while jobs still use its settings.

| Type | Config fields | Defaults |
|---|---|---|
| Local | `path` | `./data/backups` |
| S3 | `endpoint`, `bucket`, `region`, `access_key`, `secret_key`, `prefix`, `use_ssl` | region `us-east-1` |
| SFTP | `host`, `port`, `username`, `password`, `private_key`, `path` | port 22, path `/backups` |
| SMB | `host`, `port`, `share`, `username`, `password`, `path` | port 445, path `backups` |
| WebDAV | `url`, `username`, `password`, `path` | path `/backups` |

Archives are written to `jobs/<job-name>/<UTC timestamp>-r<run id>.tar.gz`, with `.age` appended when the run is encrypted. The job name is lowercased and every character other than `a-z`, `0-9` and `-` becomes `-`.

<!-- update after: backups: scope hooks, limit targets, check sftp keys; fix bind mounts -->
<!-- update after: backups: run in the background, set the time zone, use saved targets -->
Know the limits of each target type before you rely on it:

- **Local**: a relative path is resolved against the server's working directory, which is `/` under the systemd unit that `install.sh` writes. Always enter an absolute path, such as `/var/lib/dockmesh-backups`, and keep it outside the data directory. Otherwise every system backup also contains all earlier backups. A local target on the server's own disk does not protect you against losing that disk.
- **S3**: the target form has no TLS switch and no prefix field, and `use_ssl` defaults to false. A target created in the UI talks plain HTTP to the endpoint. Set `"use_ssl": true` (and `prefix` if you need one) through the API. Each archive is first written to a temporary file on the server and then uploaded in one request, so the server needs free temporary disk space as large as the archive.
- **SFTP**: the server's host key is not verified, so anyone who can intercept the connection can impersonate the server. Only use SFTP on networks you trust. The form only offers password authentication; `private_key` (PEM) can be set through the API.
- **SMB**: does not work when the Dockmesh server runs on Linux. The job folder is never created and listing returns wrong paths, so retention deletes nothing. Do not use SMB targets there.
- **WebDAV**: the whole archive is held in server memory before upload, and requests time out after 30 seconds. **Test connection** always reports success for WebDAV. Use it only for small archives.
- **Inline port**: the inline form in the job wizard has a port field only for SFTP and sends it as text, which the server rejects when the job runs. For a non-default SFTP port, or any SMB port, create a saved target and pick it.

## Creating a job

Click **New backup job** on the **Jobs** tab. The wizard asks for:

1. **Job name** and **Source**: pick a stack or a volume, or **Whole dockmesh**.
2. **Frequency**: hourly, every 6 hours, daily, weekly, or a raw 5-field cron expression.
3. **Write to target**: a saved target or an inline one.
4. Encryption, retention and pre-backup hooks.

<!-- update after: backups: run in the background, set the time zone, use saved targets -->
Schedules use the server's local time zone (the time zone of the `dockmesh` process). A new run starts even if the previous run of the same job is still going, so pick intervals that leave enough room.

### Retention

Retention runs after each successful run and has two settings, **Keep at least N runs** (`retention_count`) and **Max age (days)** (`retention_days`). 0 turns a rule off. Both rules are applied independently: an archive is deleted if it is not among the newest N, or if it is older than the maximum age. Despite the hint in the wizard, an archive among the newest N is still deleted once it is older than the maximum age.

<!-- update after: backups: keep retention per job, fail on partial or missing data -->
Retention works on files in the target, not on the run history. It deletes every file under `jobs/<job-name>` that matches the rules, including archives from failed runs. On S3 the match is a plain prefix, so the retention of a job named `db` also deletes archives of `db-prod`. Two jobs whose names normalise to the same folder share one retention pool. Give every job a distinct name that is not a prefix of another job's name.

### Encryption

With **Encrypt at rest with age** on, the archive is encrypted to the server's age key (`secrets.age-key` in the data directory), the same key that protects stack `.env` files. There is no per-job key or passphrase.

Encryption only happens when secrets encryption is enabled (`DOCKMESH_SECRETS_ENCRYPT_ENV`, on by default). With it off, the archive is written unencrypted but still gets the `.age` suffix and is listed as encrypted.

Keep a copy of the key outside the server: without it nobody can decrypt the archives, Dockmesh included. How to export it is covered in [Disaster recovery](./disaster-recovery.md#export-the-age-key). If you rotate the key (**Settings** → **Rotate key**), Dockmesh can no longer decrypt archives made with the old key. Only one previous key is kept, as `secrets.age-key.old`, so export the key again after every rotation and keep the old copies.

## Hooks

<!-- update after: backups: scope hooks, limit targets, check sftp keys; fix bind mounts -->
A hook is a command that Dockmesh runs with `docker exec` in a named container on the job's host. Pre-hooks run before the archive is written, post-hooks after it. Each hook has a 5-minute timeout, and up to 16 KB of its output goes into the run log. A failing pre-hook fails the run before anything is written. A failing post-hook marks the run as failed even though the archive was written, and failed runs cannot be restored from the UI.

The wizard's presets (**PostgreSQL dump**, **MySQL dump** and so on) do not work as they ship. They target fixed container names such as `postgres` that Dockmesh never creates (Dockmesh names containers `<stack>-<service>-1`). The MySQL and MariaDB presets run without a shell, so `>` is passed to the program as an argument. And the dump goes to `/tmp` in the container, which is not part of any backed-up volume.

A working hook has three properties:

- `container` is the real container name, e.g. `analytics-db-1`.
- `cmd` runs through `sh -c` when it needs redirection or environment variables.
- The dump is written into a named volume of the same stack, so a `stack` job includes it. A dedicated volume such as `dumps:/dumps` in the service keeps it apart from the live data directory.

The wizard cannot enter custom hooks, and saving a job in the wizard replaces its pre-hooks with the selected presets and clears all post-hooks. Set hooks through the API and do not re-save the job in the wizard afterwards. With an [API token](../features/api-tokens.md) that has `backups.view` and `backups.update`:

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

For MySQL or MariaDB the command would be `["sh", "-c", "mysqldump -u root -p\"$MYSQL_ROOT_PASSWORD\" --all-databases > /dumps/all.sql"]`. The variable is expanded by the container's shell.

## Default system backup

`PUT /api/v1/backups/system/enabled` with `{"enabled": true}` creates a job named `dockmesh-system`: system source, daily at `0 3 * * *`, 14 days retention, local target `./data/backups`, encrypted when secrets encryption is on. If a job with a system source already exists, no new job is created. `{"enabled": false}` disables it. The UI has no switch for this; you can also create the same job in the wizard with **Whole dockmesh**. Change its target to an absolute path, or better an off-host target, for the reasons given under [Targets](#targets).

Installs upgraded from older versions may still have an auto-created `dockmesh-system` job. The **Jobs** tab flags it for review with **Keep** and **Disable** buttons.

## Running and monitoring

**Run now** (on a job card, in the job drawer and on the job page) starts a run immediately and requires `backups.update`.

<!-- update after: backups: run in the background, set the time zone, use saved targets -->
Despite the confirmation text, the run is not started in the background: it runs inside the HTTP request. Keep the page open until it finishes, because closing the tab or losing the connection cancels the run. **Run all due** starts every enabled job at once, whether it is due or not, but only while no run is `running`; a run left in `running` by a server restart disables the button. For long backups, rely on the schedule.

The **Runs** tab lists every run with status, size and duration; open one to see its log. Each finished run also posts a success or failure entry to the notification bell. A run that was interrupted by a server restart stays in the `running` state.

## Restore

Click **Restore** on a successful run (in the **Runs** tab or on the run's page) and type the target name to confirm.

<!-- update after: backups: restore onto the origin host, verify first, replace or merge -->
Restores always write to the local host, whichever host the backup came from. They merge into what is there: files in the archive overwrite existing files, and everything else stays. Dockmesh does not stop containers before a restore, regardless of what the UI says. Stop the stack first and redeploy it afterwards.

<!-- update after: restore: stage system restores and swap them in at startup -->
- **Volume run**: extracted into the volume you name (the **Runs** tab suggests `<volume>-restored`; the run page starts empty). The volume is created if it does not exist.
- **Stack run**: the stack files are written to `stacks/<name>/` and the volumes in the archive are restored under their original names, which are usually `<original-stack>_<volume>`. Restoring under a different stack name therefore still writes into the original stack's volumes. A failed volume shows up as a warning while the restore reports success. The stack is not redeployed.
- **System run**: the UI shows **Restore**, but it only unpacks the archive into a Docker volume with the name you type; it does not restore the server. Use `dockmesh restore` on the command line, see [Disaster recovery](./disaster-recovery.md).

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

<!-- update after: rbac: scope read endpoints, limit grants, gate sensitive downloads -->
| Permission | Allows |
|---|---|
| `backups.view` | List jobs, targets and runs, read run logs, **download any archive decrypted**, and read target configurations including their passwords and keys |
| `backups.create` | Create jobs and targets, test target settings |
| `backups.update` | Edit jobs and targets, **Run now**, the system backup switch |
| `backups.delete` | Delete jobs and targets |
| `backups.restore` | Restore, verify, and export the age key |

`backups.view` is effectively full read access to your data: a system archive contains the database, the JWT secret and the age key. Every built-in role, viewer included, has `backups.view`, and backup permissions are not limited by host or role scope. Give users you don't fully trust a custom role without it (see [Permissions](../concepts/permissions.md)). Hooks run arbitrary commands in any container, so treat `backups.create` and `backups.update` like exec access.
