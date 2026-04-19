-- 029_stack_deploy_history.sql
-- P.12.6: persistent history of stack deploys so operators can roll
-- back to a specific past version.
--
-- Design:
--   - One row per successful Deploy. A failed deploy leaves nothing here.
--   - compose_yaml captures the exact file that was deployed. Rollback
--     overwrites the stack's compose.yaml with this column and re-runs
--     Deploy through the usual handler — no special rollback path.
--   - services_json is the resolved image tag per service at deploy
--     time (e.g. {"web":"ghcr.io/x/y@sha256:…"}). Written best-effort
--     from DeployResult so operators can see what was actually pulled.
--   - env is deliberately NOT captured: it can contain plaintext
--     secrets the user has encrypted at rest; copying it into plain
--     SQL would undermine that. Rollback keeps the current env.
--   - note is whatever the caller wanted to record (git commit, release
--     tag, "manual hotfix", whatever). Optional.
--
-- Retention is unbounded for MVP. A future slice can add a rolling
-- "keep last 20 per stack" pruner if the table gets unwieldy in
-- production; for now, rows are cheap and operators sometimes want
-- to go back further than 20.
CREATE TABLE IF NOT EXISTS stack_deploy_history (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    stack_name    TEXT     NOT NULL,
    host_id       TEXT     NOT NULL,
    compose_yaml  TEXT     NOT NULL,
    services_json TEXT,                    -- JSON: [{"service":"web","image":"…"}]
    note          TEXT,
    deployed_by   TEXT,                    -- users.id (TEXT, UUID-style), nullable for system/api-token-no-user
    deployed_at   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_stack_deploy_history_stack
    ON stack_deploy_history(stack_name, deployed_at DESC);
