# Security Policy

## Supported versions

Dockmesh hasn't reached 1.0 yet. Security fixes go into the latest release only; there are no
backports to older versions. Upgrading in place keeps your data and settings
(`curl -fsSL https://get.dockmesh.dev | sudo bash`).

| Version              | Supported |
|----------------------|-----------|
| latest 0.x release   | yes       |
| older releases       | no        |

Releases that fix a security issue are marked `security` in their release notes and in the
`release-manifest.json` attached to the release, and the issue is published as a GitHub
security advisory.

## Release signatures

From v0.6.0 on, every release, release candidates included, has a `checksums.txt.minisig`: a
[minisign](https://jedisct1.github.io/minisign/) signature over `checksums.txt`, whose trusted
comment names the release tag (`dockmesh v0.6.0 checksums.txt`). `install.sh` checks it before
it trusts any checksum. The public keys:

```
untrusted comment: minisign public key 01A0941E358E1C5A
RWRaHI41HpSgAcJDtFP8X2HnLIjHDLUdnj0GyCoGiGCdDW1rlLDWXzK1
untrusted comment: minisign public key 73BBC751F8715F03
RWQDX3H4Uce7c0kKscCE0WRymm/CkSETtzGn4IKKN3tdJyfO9y6ZyuLA
```

`01A0941E358E1C5A` signs every release. `73BBC751F8715F03` is kept offline and only signs the
release that replaces the first key; such a release says so in its notes. Check a download with

```bash
minisign -Vm checksums.txt -P RWRaHI41HpSgAcJDtFP8X2HnLIjHDLUdnj0GyCoGiGCdDW1rlLDWXzK1
sha256sum -c --ignore-missing checksums.txt
```

See [Verify a release](docs/site/operations/verify-release.md) for details.

## Reporting a vulnerability

Please do not open a public issue, discussion or pull request for a security problem.

Report it privately through GitHub:
**[Report a vulnerability](https://github.com/Dockmesh/dockmesh/security/advisories/new)**
(the "Security" tab of the repository → "Report a vulnerability"). This keeps the report,
the discussion and the fix in one private advisory, and lets us credit you when it is
published.

If you can't use GitHub, email **security@dockmesh.dev** instead.

Helpful details:

- the affected version (`dockmesh version`) and how Dockmesh is installed
- what an attacker needs (network access, an account, which permissions)
- steps to reproduce or a small proof of concept
- the impact you see
- whether and how you want to be credited

## What to expect

- **Acknowledgement** within 3 days.
- **Assessment** within 7 days: whether we can reproduce it and how severe we think it is.
- **Fix:** critical and high issues are usually fixed within 30 days, lower severities in a
  regular release. We keep you updated in the advisory or by email.
- **Disclosure:** we publish the advisory when the fixed release is out, and agree the date
  with you. The default window is 90 days from your report.
- **Credit** in the advisory and release notes, by name, handle or anonymously.

Dockmesh is maintained by a small team without a paid support contract, so these are
targets, not guarantees.

## Scope

In scope: the `dockmesh` server, the `dockmesh-agent`, the `dmctl` CLI, the web UI, the
REST and WebSocket APIs, the agent protocol, the install scripts, and default
configurations.

Out of scope: vulnerabilities in dependencies that Dockmesh doesn't expose (please report
them upstream), social engineering, physical access, volumetric denial of service, and
modified builds.

Dockmesh manages Docker, so anyone with full admin rights in Dockmesh can effectively run
code on the managed hosts. Reports are most useful when they show a user or an
unauthenticated client getting more than their role and scope allow.

## Safe harbor

We will not take legal action against good-faith research that follows this policy: test
only against installations you own or are allowed to test, don't access or change other
people's data beyond what is needed to show the issue, and give us reasonable time to fix
it before going public.
