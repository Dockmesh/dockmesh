# Dockmesh user documentation

This directory is the source of the user documentation published on
dockmesh.dev. It is versioned with the code, so the docs for a release are the
files at that release tag.

## Rule

Docs change in the same pull request as the code. If a PR changes behaviour a
user can see (a setting, a default, a CLI flag, a UI label, a limitation), it
updates the affected pages here as well. Describe what the code does, not what
is planned.

## Format

- One Markdown file per page: `docs/site/<section>/<page>.md`, kebab-case names.
- Each page starts with frontmatter containing exactly `title` and
  `description`:

  ```markdown
  ---
  title: Backups and restore
  description: Back up volumes, stacks and the Dockmesh server, and restore them.
  ---
  ```

  The title is rendered as the page heading, so the body starts with prose and
  uses `##` and below.
- `nav.json` is the only source for the sidebar: order, grouping and the
  sidebar label of every page. Every page appears in it exactly once.

  ```json
  {"version":1,"sections":[{"title":"Getting started","items":[{"title":"Installation","path":"getting-started/installation.md"}]}]}
  ```

- Links between pages are relative paths to the `.md` file, for example
  `../operations/backups.md#restore`. No absolute links to the docs site.
- Images go in `docs/site/assets/` and are linked relatively.
- Plain Markdown only. Notes are blockquotes starting with `**Note:**` or
  `**Warning:**`; no site-specific components.

## Checking

```sh
go run ./scripts/docscheck
```

checks that `nav.json` and the pages match, that every page has its
frontmatter and that relative links resolve. The same check runs as part of
`go test ./...`.
