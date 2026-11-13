---
title: Verify a release
description: Check the minisign signature of a Dockmesh release before you install it by hand.
---

Every release from v0.6.0 on, release candidates included, has a `checksums.txt.minisig` next to its `checksums.txt`. It is a [minisign](https://jedisct1.github.io/minisign/) signature over `checksums.txt`, and `checksums.txt` lists the SHA-256 of every archive and script of the release. Checking the signature first and the checksums second proves that the files come from the Dockmesh maintainers and were not changed on the way.

`install.sh` does both checks on its own, see [What the installer checks](#what-the-installer-checks). This page is for manual installs and for checking a download yourself.

## Public keys

| Key ID | Public key | Use |
|---|---|---|
| `01A0941E358E1C5A` | `RWRaHI41HpSgAcJDtFP8X2HnLIjHDLUdnj0GyCoGiGCdDW1rlLDWXzK1` | Signs every release. |
| `73BBC751F8715F03` | `RWQDX3H4Uce7c0kKscCE0WRymm/CkSETtzGn4IKKN3tdJyfO9y6ZyuLA` | Rotation key, kept offline. It signs only the release that replaces the first key. |

The same keys are built into `install.sh` and into Dockmesh. Compare them with the keys in [`SECURITY.md`](https://github.com/Dockmesh/dockmesh/blob/main/SECURITY.md) in the repository; a key you got from the same place as the download proves nothing.

## Check a download

Install minisign (`apt install minisign`, `dnf install minisign` from EPEL, `brew install minisign`), then:

```bash
TAG=v0.6.0
ARCH=amd64   # or arm64
base="https://github.com/dockmesh/dockmesh/releases/download/$TAG"
curl -fLO "$base/dockmesh_linux_$ARCH.tar.gz"
curl -fLO "$base/checksums.txt"
curl -fLO "$base/checksums.txt.minisig"

minisign -Vm checksums.txt -P RWRaHI41HpSgAcJDtFP8X2HnLIjHDLUdnj0GyCoGiGCdDW1rlLDWXzK1
sha256sum -c --ignore-missing checksums.txt
```

minisign prints `Signature and comment signature verified` and the trusted comment. The trusted comment must name the release you downloaded:

```
Trusted comment: dockmesh v0.6.0 checksums.txt
```

A signature is only valid for the tag in its trusted comment. If the tag differs, someone is serving you the files of another release; don't install them.

If the release was signed with the rotation key, the first command fails with a key ID mismatch; run it again with `-P RWQDX3H4Uce7c0kKscCE0WRymm/CkSETtzGn4IKKN3tdJyfO9y6ZyuLA`. Then read the release notes, which announce the new key.

## What the installer checks

Before it trusts any checksum, `install.sh` downloads `checksums.txt.minisig` and checks it with OpenSSL 3 against the two built-in keys: the signature over `checksums.txt`, the signature over the trusted comment, and that the trusted comment names the tag it is installing. Only then does it compare the archive with its entry in `checksums.txt`. Any failure stops the install before anything is unpacked.

OpenSSL 3 ships with current Ubuntu, Debian and RHEL releases. Without it (OpenSSL 1.1, LibreSSL on macOS) the installer stops during its checks and names the override.

| Variable | Effect |
|---|---|
| `DOCKMESH_SKIP_SIGNATURE=1` | Trusts `checksums.txt` without checking its signature and prints a warning. The archive is still compared with `checksums.txt`. Needed for releases before v0.6.0, which have no signature, and on hosts without OpenSSL 3; check the signature yourself as above first. |
| `DOCKMESH_SKIP_CHECKSUM=1` | Skips the signature and the checksum. |

For example, to install an older release that has no signature:

```bash
curl -fsSL https://get.dockmesh.dev | sudo DOCKMESH_SKIP_SIGNATURE=1 bash -s -- --version v0.5.3
```

## If a key changes

A new release key is announced in the release notes of a release signed with the rotation key `73BBC751F8715F03`, and the keys in `install.sh`, in Dockmesh and on this page change with it. A release signed with any other key is not genuine.
