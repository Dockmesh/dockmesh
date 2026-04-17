-- 023_scope_tags.sql
-- Role scoping by host tag (P.11.3). A user's role grants it
-- permissions; scope_tags narrow those permissions to hosts whose
-- host_tags include at least one of the listed tags. NULL / empty
-- scope_tags means "all hosts" — backwards compatible with the
-- pre-P.11.3 behavior.
--
-- Stored as a JSON array of strings. No FK into host_tags because the
-- tags referenced may exist on zero hosts (still valid, just never
-- matches) or on hosts we haven't seen yet.
ALTER TABLE users ADD COLUMN scope_tags TEXT;

-- OIDC default scope for auto-provisioned users. When a user first
-- logs in via an OIDC provider, the new user row is created with this
-- scope (in addition to default_role). Admins can still edit per-user
-- afterwards.
ALTER TABLE oidc_providers ADD COLUMN default_scope_tags TEXT;
