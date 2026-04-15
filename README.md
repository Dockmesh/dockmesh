<p align="center">
  <img src="docs/logos/dockmesh-github.svg" alt="Dockmesh" width="480" />
</p>

# Dockmesh

[![License: AGPL v3](https://img.shields.io/badge/License-AGPL_v3-blue.svg)](https://www.gnu.org/licenses/agpl-3.0)

**100% Open-Source Container Management. Single binary. No paywalls.**

Dockmesh is a lightweight container management platform for Docker hosts and
fleets. It ships as a single Go binary with an embedded SvelteKit UI, talks
directly to the Docker SDK, and treats the filesystem as the source of truth
for stack configs.

> Status: early skeleton. Phase 1 (MVP) in progress.

## Quick Start

```bash
docker run -d --name dockmesh \
  -p 8080:8080 \
  -v /var/run/docker.sock:/var/run/docker.sock:ro \
  -v dockmesh-data:/app/data \
  -v dockmesh-stacks:/app/stacks \
  ghcr.io/dockmesh/dockmesh:latest
```

Then open <http://localhost:8080>.

## Features (Phase 1 MVP)

- Single binary — Go backend serves embedded SvelteKit frontend
- Stack management — `stacks/<name>/compose.yaml` is the source of truth
- Container / image / volume dashboard
- Argon2id + JWT auth with refresh token rotation
- SQLite by default (WAL), Postgres optional
- Dark / light mode, mobile-friendly
- AGPL-3.0 — RBAC and SSO are free

### Roadmap

- Phase 2: Caddy reverse proxy + Grype vulnerability scanner
- Phase 3: outbound-only remote agents over gRPC

## Screenshots

> _Coming soon._

## Documentation

- [Architecture overview](docs/architecture/overview.md)
- [Getting started (dev)](docs/development/getting-started.md)
- [Example config](configs/dockmesh.example.yaml)

## Contributing

Issues and PRs welcome. Please:

1. Run `make lint` and `make test` before opening a PR.
2. Keep the AGPL-3.0 license headers intact.
3. Open an issue first for larger changes so we can align on scope.

## License

[AGPL-3.0](LICENSE) — if you modify Dockmesh and run it as a network service,
you must share your modifications under the same license.
