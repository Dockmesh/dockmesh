-- P.11.12 — stack templates.
--
-- Templates are compose.yaml snippets with {{placeholders}} that the
-- server substitutes at deploy time. Built-in templates ship via
-- go:embed and get upserted on each boot; user-created templates live
-- in rows with builtin=0 and can be edited/deleted freely.
--
-- Note: the spec mentions a stack_template_uses table to track which
-- stacks were deployed from which template. That's nice-to-have but
-- not a blocker for the feature, so we skip it in v1 — the audit log
-- already records template.deploy events with the template id.
CREATE TABLE IF NOT EXISTS stack_templates (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    slug        TEXT NOT NULL UNIQUE,        -- stable identifier, e.g. "postgres-16"
    name        TEXT NOT NULL,               -- human label, e.g. "PostgreSQL 16"
    description TEXT,
    icon_url    TEXT,                         -- optional image URL (external or /static/…)
    compose     TEXT NOT NULL,               -- compose.yaml with {{param}} placeholders
    env_tmpl    TEXT,                         -- optional .env template
    parameters  TEXT NOT NULL DEFAULT '[]',   -- JSON array of ParamDef
    author      TEXT,
    version     TEXT,
    builtin     INTEGER NOT NULL DEFAULT 0,
    created_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_stack_templates_slug ON stack_templates(slug);
