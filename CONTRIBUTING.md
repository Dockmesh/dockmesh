# Contributing to Dockmesh

Thanks for wanting to contribute. This is a short, honest guide so you
don't waste time guessing what we expect.

## Before you open a PR

1. **Open an issue first** for anything larger than a one-file fix. We
   want to align on scope before you invest hours. Small bug fixes and
   doc typos can go straight to a PR.
2. **Run the checks locally:**
   ```bash
   make lint    # golangci-lint + svelte-check
   make test    # Go + Playwright E2E
   ```
   CI runs the same set — failing checks block merge.
3. **Keep the diff tight.** A focused change is easier to review than
   one that also reformats twelve unrelated files.
4. **Commit messages** follow the project style: imperative mood, one
   short subject line, a blank line, then a "why this change" paragraph.
   `git log` already has plenty of examples.

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
