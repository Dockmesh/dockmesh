---
title: Disaster recovery
description: What to keep safe, how to export the age key, and how to rebuild a Dockmesh server from a system backup.
---

When the Dockmesh server goes down, containers keep running: on the local host Docker runs them without Dockmesh, and remote agents keep retrying the connection until a server is back. What you lose is the management layer and its state. This page covers what you need to bring that back.

## What to keep safe

Paths below are for the default `install.sh` layout (`/var/lib/dockmesh`). If `/data` existed at install time, `install.sh` used `/data` instead; replace the paths and pass `--env-file /data/dockmesh.env` to `dockmesh` commands. Other layouts follow `DOCKMESH_DB_PATH`: the data directory is the directory that contains the database file.

| Path | Contents | Lost means |
|---|---|---|
| `data/dockmesh.db` | Users, roles, hosts, backup jobs, targets and runs, audit log, settings | Everything except stack files |
| `data/secrets.age-key` | age key for stack `.env.age` files, the encrypted credentials in the database (SSO and LDAP secrets, registry passwords, Git credentials, backup target credentials, secret global variables) and encrypted backups | Encrypted archives and secrets cannot be decrypted |
| `data/secrets.age-key.history` | Earlier keys, kept after each rotation | Archives made before a rotation cannot be decrypted |
| `data/secrets.env` | JWT signing secret | All sessions end; users sign in again |
| `data/agents-ca.crt`, `agents-ca.key`, `agents-server.crt`, `agents-server.key` | Agent CA and the server's mTLS certificate | Every agent must re-enroll |
| `data/audit-genesis.sha256` | Anchor of the audit hash chain | Chain can't be checked against its origin |
| `stacks/` | `compose.yaml`, `.env` / `.env.age`, metadata per stack, and the data of relative bind mounts of stacks on the server's host | Stack definitions |
| `dockmesh.env` | Server configuration (`DOCKMESH_*` variables) | Settings made only there, e.g. `DOCKMESH_BASE_URL` |

A system backup covers the database, `stacks/` and `data/` (without `data/backups`). It does **not** include `dockmesh.env` (it sits one level above `data/`) or any Docker volume. Keep a copy of `dockmesh.env` with your notes, and back up stateful stacks with their own stack jobs.

## Export the age key

Encrypted backups are encrypted to the server's own key, and that key is inside the system backup. An encrypted system backup can therefore only be opened with a copy of the key that you stored somewhere else. Store it in a password manager or another safe place outside the server, and again after every key rotation.

In the UI, go to **Settings** → **Encryption key** and click **Download key backup**. It saves `dockmesh-backup-key.txt`, which holds the current key followed by every earlier key, so archives from before a rotation can be opened with the same file. Downloading needs the **Export the backup encryption key** extra on an assignment on everything (admins have it), and every download is written to the audit log.

The same file comes from the API, for example with an [API token](../features/api-tokens.md):

```bash
umask 077
curl -fsS -H "Authorization: Bearer dmt_xxxxxxxx" \
  https://dockmesh.example.com/api/v1/system/backup-key/export -o dockmesh-backup-key.txt
```

On the server itself, `sudo cat /var/lib/dockmesh/data/secrets.age-key` shows the current key only. All forms are standard age identity files and work with `age -d -i`.

## Plan before you need it

- Run a daily system backup to an **off-host** target (S3, SFTP, SMB or WebDAV). A local target on the server's disk is gone together with the server. See [Backups](./backups.md#default-system-backup).
- Add stack jobs for every stack with data in volumes.
- Keep the key file and `dockmesh.env` outside the server.
- Note which Dockmesh version you run (`dockmesh version`). Restore onto the same or a newer version, because database migrations only go forward.
- Run a test restore on a spare machine now and then. `POST /api/v1/backups/runs/{id}/verify` gives a quick check of a system run without restoring it.

## Rebuild a server

There are three ways to restore a system backup. All of them unpack the archive next to the live files, run the restore checks, and swap the restored files in before the database is opened. The replaced files are kept in `<data>/.pre-restore/<time>` and `<stacks>.pre-restore-<time>`; the **Backups** page lists them to admins with a **Delete** button.

- **Setup wizard** of a fresh install: **Restore from a backup instead**, upload the system backup and, for an encrypted backup, the key file you exported. Dockmesh restarts, closes the setup wizard and opens the login page. This is the quickest way to rebuild a server.
- **Backups** page of a running server (admins): **Restore system…** on a system run, or **Restore system from file…** on the **Runs** tab. See [Restoring the whole system](./backups.md#restoring-the-whole-system).
- **Command line**, with the server stopped: `dockmesh restore`, below.

| Flag | Meaning |
|---|---|
| `--from <path>` | System archive (`.tar.gz` or `.tar.gz.age`), required |
| `--key <file>` | Backup key file exported from the old server, needed for encrypted archives |
| `--force` | Restore over a database that already has users |
| `--dry-run` | Only walk the archive and print `would restore N files, M bytes` |

Like other subcommands, `restore` reads `/var/lib/dockmesh/dockmesh.env` (then `/data/dockmesh.env`) when `DOCKMESH_DB_PATH` is not set in the environment. Pass `--env-file <path>` for any other location.

### 1. Install Dockmesh on the new machine

```bash
curl -fsSL https://get.dockmesh.dev | sudo DOCKMESH_VERSION=v0.5.0 bash
```

Use the version you ran before, or newer. To restore through the setup wizard, open the server's URL now and pick **Restore from a backup instead**; then continue with step 4 to carry over `dockmesh.env`, and with step 5.

For the command line, stop the server and don't finish the setup wizard. If you already did, you need `--force` in step 3.

```bash
sudo systemctl stop dockmesh
```

### 2. Get the archive and the key

Copy the latest system archive from your backup target, e.g. `jobs/1-dockmesh-system/20261029-030000-r412.tar.gz.age`, and the key file you exported (`dockmesh-backup-key.txt`). An archive without `.age` is not encrypted and needs no key.

### 3. Restore

```bash
sudo dockmesh restore --from 20261029-030000-r412.tar.gz.age --key dockmesh-backup-key.txt --dry-run
sudo dockmesh restore --from 20261029-030000-r412.tar.gz.age --key dockmesh-backup-key.txt
```

The restore prints each check as `[ ok ]`, `[warn]` or `[FAIL]`. The database must open and migrate and contain at least one admin; otherwise nothing is replaced. A missing CA certificate or key (`agents-ca.crt`, `agents-ca.key`), audit genesis, `secrets.env` or stacks directory is a warning. The server certificate and the age key are not checked. Then the restored stacks directory replaces the live one as a whole; in the data directory, each file or folder in the archive replaces the live one and everything else stays (`data/backups` is never replaced). The command ends with `restore complete` and the number of files and bytes.

If the command stops halfway, run it again or start the server: the swap finishes on the next start.

### 4. Fix configuration and ownership

After a command-line restore, the restored files belong to root. The fresh install also left `DOCKMESH_SETUP_FORCE=true` in its env file; the server ignores it once an admin exists, but warns about it on every start.

```bash
sudo sed -i '/^DOCKMESH_SETUP_FORCE=/d' /var/lib/dockmesh/dockmesh.env
sudo chown -R dockmesh:docker /var/lib/dockmesh/data /var/lib/dockmesh/stacks
```

Carry over the settings from your saved `dockmesh.env` (`DOCKMESH_BASE_URL`, `DOCKMESH_AGENT_PUBLIC_URL`, `DOCKMESH_AGENT_SANS`, proxy settings and so on). Settings changed in the UI are in the database and came back with it. Then check and start:

```bash
sudo -u dockmesh dockmesh doctor
sudo systemctl start dockmesh
```

Sign in with an account of the restored install.

### 5. Bring back stacks and data

A system restore brings back stack definitions, not containers or volume data. For each stateful stack, open its latest stack run under **Backups** → **Runs** and click **Restore…** (see [Backups](./backups.md#restore)). Then deploy the stack. Stacks without volumes only need a deploy.

Point DNS or your load balancer for the server's public name at the new machine, so agents and users find it.

## Agents after a restore

Agents trust the CA they received at enrollment and connect to the address they were given. If the restored `data/` directory contains the old `agents-ca.*` and `agents-server.*` files and the server is reachable under the same name, agents reconnect on their own.

If the CA files were missing, Dockmesh creates a new CA on first start and the agents' certificates no longer match. The same happens after `dockmesh ca rotate`. Re-enroll every agent, in one of two ways:

- `sudo -u dockmesh dockmesh ca rotate --reissue-all-agents` issues a new CA, sets every agent to pending with a fresh token, and prints one install command per agent. Run each command on its host.
- Per host: **Revoke…** the host under **Hosts**, add it again with **Add host**, and run the new install command on the agent host. The installer removes the old certificates there before enrolling.

Agent certificates are valid for 365 days. An agent of v0.5.0 or later renews its certificate on its own in the last 30 days; admins get a notice when a certificate is 14 days from expiry and has not been renewed.

## Other failures

- **Single stack broken**: stop the stack, restore its last good stack run with **Replace**, deploy. Replace makes the volumes match the backup and leaves them untouched if the restore fails; take a fresh backup of the current state first if you might want it back.
- **Remote host lost**: restore the stack runs of that host onto another host by picking it under **Host** in the restore dialog, then deploy the stacks there. Restoring onto an agent host with **Replace** needs an agent of the same release as the server. [Stack migration](../features/migration-and-drain.md) is no help here: it needs the source host and refuses stacks with named volumes.
- **Database damaged**: stop the server, then run `sqlite3 dockmesh.db "PRAGMA integrity_check;"`. If it reports errors, restore the database from a system backup as above.
