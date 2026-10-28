# Contributing to dockmesh

Thanks for wanting to contribute. This is a short, honest guide so you
don't waste time guessing what we expect.

## Workflow

Everything lands on `main` through a pull request; nobody pushes to it directly.

1. Fork the repo (or create a branch if you have write access) and branch
   from an up-to-date `main`.
2. Make your change, commit, push, and open a PR against `main`.
3. The required checks run on every PR. They must be green before it can
   be merged. A maintainer reviews and merges.
4. PRs are **squash-merged**: the PR title becomes the commit subject on
   `main`, so the individual commits on your branch don't need to be tidy.

### PR titles

Use `area: what`, short and lowercase, no trailing period, no emoji:

```
proxy: fix dark-mode metrics label
stacks: reject compose files without services
ci: add pull request checks
```

Keep the title to about 70 characters. In the description say what
changed and why, and how you tested it.

### Required checks

| Check | What it runs |
|---|---|
| `go` | `go vet ./...` and `go test -race ./...` (includes the OpenAPI drift test) |
| `lint` | golangci-lint, reporting only issues your change introduces |
| `web` | `svelte-check` and the frontend build |
| `build` | server, agent and `dmctl` for linux and macOS on amd64 and arm64 |
| `licenses` | Go and npm production dependencies must use AGPL-compatible licenses |

CodeQL also scans each PR.

### Changelog

If users will notice your change, add a line under `## [Unreleased]` in
`CHANGELOG.md`; the format is described at the top of that file. The
`changelog` check fails otherwise. Changes users won't notice (CI,
refactoring, tests) get the `no-changelog` label instead.

### Running the checks locally

```bash
# Go: the embed target must exist once, then vet + tests
mkdir -p cmd/dockmesh/web_dist
go vet ./...
go test -race ./...        # -race needs cgo (gcc); drop the flag if you have none

# Frontend
cd web && npm ci && npm run check && npm run build && cd ..

# Lint
golangci-lint run          # v1.64.x, as configured in .golangci.yml
```

`make test` and `make lint` wrap the same commands. The Playwright suite
(`make test-e2e`) needs a running Dockmesh instance and is not part of
the required checks.

## Before you open a PR

1. **Open an issue first** for anything larger than a one-file fix. We
   want to align on scope before you invest hours. Small bug fixes and
   doc typos can go straight to a PR.
2. **Keep the diff tight.** A focused change is easier to review than
   one that also reformats twelve unrelated files.
3. **Commit messages** follow the project style: a short `area: what`
   subject, and a body only when the why isn't obvious. `git log` already
   has plenty of examples.

## License on contributions

By opening a pull request you agree that your work is licensed under
**AGPL-3.0-only**, the same license as the rest of the project. We
don't ask for a CLA. If you can't license your work under AGPL, please
don't open a PR.

## What makes a good PR

- One problem per PR. If you find a second bug while fixing the first,
  open a separate issue or PR — don't bundle.
- Tests for new behaviour, a reproducer test for bug fixes.
- If you touch a handler in `internal/api/handlers/`, update the
  OpenAPI spec in `internal/api/openapi/openapi.yaml` in the same
  commit. CI enforces this via `TestOpenAPIDriftAgainstRoutes`.
- UI changes: run a manual smoke pass before requesting review. Type
  checks and unit tests verify code correctness, not feature correctness.

## What we'll probably push back on

- Adding a config flag nobody asked for. Sensible defaults > endless
  toggles.
- Renaming things for style. We care about clarity, not consistency
  with your preferred convention.
- Re-architecting something without discussing it first. See "open an
  issue first" above.

## Getting help

Stuck during setup? Have a question that isn't a bug? Open a GitHub
Discussion — not an issue. Issues are for actionable work.

Thanks for being here.
