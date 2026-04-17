import { auth } from './stores/auth.svelte';

const BASE = '/api/v1';

export type Severity = 'critical' | 'high' | 'medium' | 'low' | 'negligible' | 'unknown';

export interface ScanVuln {
  id: string;
  severity: Severity;
  package: string;
  version: string;
  fixed_in?: string;
  type?: string;
  url?: string;
}

export interface ScanReport {
  image: string;
  scanner: string;
  scanner_version?: string;
  scanned_at: string;
  summary: {
    critical: number;
    high: number;
    medium: number;
    low: number;
    negligible: number;
    unknown: number;
  };
  vulnerabilities: ScanVuln[];
}

export interface UpdateResult {
  container_id: string;
  container_name: string;
  image: string;
  old_digest: string;
  new_digest: string;
  updated: boolean;
  rollback_tag?: string;
  history_id?: number;
}

// UpdatePreview mirrors internal/updater.UpdatePreview — non-destructive
// lookup of what a container's next update would pull plus any Docker
// Hub / GitHub release metadata we can dig up. Used by the container
// detail page's "Updates" tab to let the user see what's waiting without
// actually touching the image.
export interface GitHubRelease {
  tag: string;
  name: string;
  url: string;
  body: string;
  published?: string;
}
export interface UpdatePreview {
  image: string;
  current_digest?: string;
  current_created?: string;
  remote_last_updated?: string;
  remote_size?: number;
  docker_hub_url?: string;
  github_url?: string;
  latest_release?: GitHubRelease;
  warnings?: string[];
}

export interface MetricsSample {
  ts: number;
  cpu_percent: number;
  mem_used: number;
  mem_limit: number;
  net_rx: number;
  net_tx: number;
  blk_read: number;
  blk_write: number;
}

export interface HostInfo {
  id: string;
  name: string;
  kind: 'local' | 'agent' | 'all';
  status: 'online' | 'offline' | 'pending' | 'revoked';
  tags?: string[];
}

// SystemMetrics is the host-level CPU / memory / disk / uptime snapshot
// returned by GET /api/v1/system/metrics. In all-mode the response is a
// FanOutResponse<SystemMetrics>, one row per host, with host_id and
// host_name flattened alongside the metrics fields via backend struct
// embedding.
// BackupStatus is the compact state record used by the sidebar "last
// backup" pill and the Settings > System automated-backup section.
// state:
//   never     — default job doesn't exist or has never run
//   ok        — most recent run succeeded ≤ 36 h ago
//   stale     — most recent run succeeded but is older than 36 h
//   failed    — most recent run's status is not "success"
//   disabled  — default job exists but is disabled
export interface BackupStatus {
  state: 'never' | 'ok' | 'stale' | 'failed' | 'disabled';
  enabled: boolean;
  job_exists: boolean;
  last_run_at?: string;
  last_status?: string;
  last_error?: string;
  last_size_bytes?: number;
  age_seconds?: number;
}

// StackDeployment tracks which host a stack is deployed on (P.7).
export interface StackDeployment {
  stack_name: string;
  host_id: string;
  host_name?: string;
  status: 'deployed' | 'stopped' | 'migrating' | 'migrated_away';
  deployed_at: string;
  updated_at: string;
}

export interface StackListEntry {
  name: string;
  compose_path: string;
  deployment?: StackDeployment;
}

// Scaling (P.8)
export interface ScaleCheck {
  service: string;
  current_replicas: number;
  has_container_name: boolean;
  has_hard_port: boolean;
  hard_port_detail?: string;
  is_stateful: boolean;
  stateful_image?: string;
  has_volumes: boolean;
}

export interface ScaleResult {
  service: string;
  previous: number;
  current: number;
  created: number;
  removed: number;
}

export interface ScalingConfig {
  enabled: boolean;
  rules: ScalingRule[];
}

export interface ScalingRule {
  service: string;
  min_replicas: number;
  max_replicas: number;
  scale_up: ThresholdConfig;
  scale_down: ThresholdConfig;
  cooldown_seconds: number;
}

export interface ThresholdConfig {
  metric: 'cpu' | 'memory';
  threshold_percent: number;
  duration_seconds: number;
}

// Migration (P.9)
export interface Migration {
  id: string;
  stack_name: string;
  source_host_id: string;
  target_host_id: string;
  status: string;
  phase?: string;
  progress?: MigrationProgress;
  started_at?: string;
  completed_at?: string;
  error_message?: string;
  initiated_by: string;
  created_at: string;
}

export interface MigrationProgress {
  current_volume?: string;
  volume_index: number;
  volumes_total: number;
  bytes_total: number;
  bytes_done: number;
  images_pulled: number;
  images_total: number;
}

export interface PreflightResult {
  passed: boolean;
  checks: Array<{ name: string; passed: boolean; detail?: string }>;
}

// Drain Host (P.10)
export interface DrainPlan {
  source_host_id: string;
  source_name: string;
  entries: DrainPlanEntry[];
  feasible: boolean;
}

export interface DrainPlanEntry {
  stack_name: string;
  target_host_id: string;
  target_name: string;
  weight_bytes: number;
  feasible: boolean;
  detail?: string;
}

export interface Drain {
  id: string;
  source_host_id: string;
  status: string;
  plan: DrainPlanEntry[];
  started_at?: string;
  completed_at?: string;
  initiated_by: string;
  created_at: string;
}

// Custom Roles (RBAC v2)
export interface CustomRole {
  name: string;
  display: string;
  builtin: boolean;
  permissions: string[];
}

export interface PermissionInfo {
  name: string;
  description: string;
}

export interface ApiToken {
  id: number;
  prefix: string;            // e.g. "dmt_A7f3X9c4"
  name: string;
  role: string;
  created_by?: number;
  created_at: string;
  expires_at?: string;
  last_used_at?: string;
  last_used_ip?: string;
  revoked_at?: string;
}

// P.11.7. Password is never returned — `has_password` tells the UI
// whether one is stored so the edit dialog can render
// "leave blank to keep existing".
export interface Registry {
  id: number;
  name: string;
  url: string;
  username?: string;
  has_password: boolean;
  scope_tags?: string[];
  last_tested_at?: string;
  last_test_ok?: boolean;
  last_test_error?: string;
  created_at: string;
  updated_at: string;
}

export interface RegistryInput {
  name: string;
  url: string;
  username?: string;
  password?: string;       // optional on update — empty = keep existing
  clear_password?: boolean; // explicit wipe; takes precedence over password
  scope_tags?: string[];
}

export interface RegistryTestResult {
  ok: boolean;
  status?: string;
  identity?: boolean;
  error?: string;
}

// P.11.16 — agent upgrade policy.
export interface AgentUpgradePolicy {
  mode: 'auto' | 'manual' | 'staged';
  stage_percent?: number;
  stage_gap_sec?: number;
  server_version: string;
  connected_total: number;
  connected_up_to_date: number;
  connected_pending: number;
  last_run_at?: string;
}

export interface AgentUpgradeInput {
  mode: 'auto' | 'manual' | 'staged';
  stage_percent?: number;
  stage_gap_sec?: number;
}

// P.11.14 — audit webhook.
export interface AuditWebhookConfig {
  url?: string;
  has_secret: boolean;
  filter_actions?: string[];
}

export interface AuditWebhookInput {
  url: string;
  secret?: string;
  clear_secret?: boolean;
  filter_actions?: string[];
}

// P.11.13 — audit retention.
export interface AuditRetentionConfig {
  mode: 'forever' | 'days' | 'archive_local' | 'archive_target';
  days?: number;
  target_id?: number;
  local_dir?: string;
}

export interface AuditRetentionPreview {
  mode: string;
  cutoff_at?: string;
  would_prune: number;
  total_rows: number;
  oldest_at?: string;
}

export interface AuditRetentionResult {
  mode: string;
  cutoff_at?: string;
  pruned: number;
  archived?: boolean;
  archive_path?: string;
  bridge_row_id?: number;
  duration_ms: number;
}

// P.11.12 — stack templates.
export interface StackTemplateParam {
  name: string;
  description?: string;
  type?: 'string' | 'number' | 'bool' | 'secret';
  default?: string;
  secret?: boolean;
  enum?: string[];
  pattern?: string;
  required?: boolean;
}

export interface StackTemplate {
  id: number;
  slug: string;
  name: string;
  description?: string;
  icon_url?: string;
  compose: string;
  env?: string;
  parameters: StackTemplateParam[];
  author?: string;
  version?: string;
  builtin: boolean;
  created_at: string;
  updated_at: string;
}

export interface StackTemplateInput {
  slug: string;
  name: string;
  description?: string;
  icon_url?: string;
  compose: string;
  env?: string;
  parameters?: StackTemplateParam[];
  author?: string;
  version?: string;
}

export interface TemplateDeployResponse {
  stack: string;
  compose: string;
  values: Record<string, string>;
  deploy_result?: unknown;
}

// P.11.11 — git-backed stacks.
export interface StackGitSource {
  stack_name: string;
  repo_url: string;
  branch: string;
  path_in_repo: string;
  auth_kind: 'none' | 'http' | 'ssh';
  username?: string;
  has_password: boolean;
  has_ssh_key: boolean;
  auto_deploy: boolean;
  poll_interval_sec: number;
  has_webhook_secret: boolean;
  last_sync_sha?: string;
  last_sync_at?: string;
  last_sync_error?: string;
  created_at: string;
  updated_at: string;
}

export interface StackGitSourceInput {
  repo_url: string;
  branch?: string;
  path_in_repo?: string;
  auth_kind?: 'none' | 'http' | 'ssh';
  username?: string;
  password?: string;
  clear_password?: boolean;
  ssh_key?: string;
  clear_ssh_key?: boolean;
  auto_deploy?: boolean;
  poll_interval_sec?: number;
  webhook_secret?: string;
  clear_webhook_secret?: boolean;
}

export interface StackGitSyncResult {
  old_sha?: string;
  new_sha: string;
  changed: boolean;
  deployed?: boolean;
  deploy_result?: unknown;
  duration_ms: number;
}

// P.11.8 — volume content browsing.
export interface VolumeEntry {
  name: string;
  type: 'file' | 'dir' | 'symlink';
  size: number;
  mode: string;
  mod_time: string;
  link_dest?: string;
}

export interface VolumeFileResult {
  content: string; // base64 (Go []byte marshals to base64)
  size: number;
  truncated: boolean;
  binary: boolean;
}

export interface SystemMetrics {
  cpu_percent: number;
  cpu_cores: number;
  cpu_used_cores: number;
  mem_percent: number;
  mem_total: number;
  mem_used: number;
  disk_percent: number;
  disk_total: number;
  disk_used: number;
  disk_path: string;
  uptime_seconds: number;
}

// FanOutResponse is the shape returned by list endpoints when called
// with ?host=all. Per-row host metadata (host_id / host_name) is
// flattened into each item via backend struct embedding, and any
// hosts that failed or timed out are reported under unreachable_hosts
// so the frontend can render a "showing N of M hosts" banner.
export interface FanOutResponse<T> {
  items: Array<T & { host_id: string; host_name: string }>;
  unreachable_hosts: Array<{ host_id: string; host_name: string; reason: string }>;
}

// isFanOut narrows a list response returned by one of the ?host=all
// endpoints. Single-host responses return a bare array; the FanOutResponse
// shape is the wrapper object. Pages that call the list with either mode
// use this to branch on shape without a second query.
//
// Not generic over T because TypeScript's union-narrowing can't cross
// a generic instantiation boundary — narrowing
// `any[] | FanOutResponse<any>` via `isFanOut<Container>` leaves the
// else branch as `any[] | FanOutResponse<any>` instead of `any[]`.
// The non-generic form narrows cleanly.
export function isFanOut(r: unknown): r is FanOutResponse<any> {
  return typeof r === 'object' && r !== null && 'items' in r && 'unreachable_hosts' in r;
}

export interface Agent {
  id: string;
  name: string;
  status: 'pending' | 'online' | 'offline' | 'revoked';
  version?: string;
  os?: string;
  arch?: string;
  hostname?: string;
  docker_version?: string;
  cert_fingerprint?: string;
  last_seen_at?: string;
  created_at: string;
  updated_at: string;
}

export interface AgentCreateResult {
  agent: Agent;
  token: string;
  enroll_url: string;
  agent_url: string;
  install_hint: string;
}

export interface TopoNetwork {
  id: string;
  name: string;
  driver: string;
  scope: string;
  internal: boolean;
  system: boolean;
  stack?: string;
}

export interface TopoContainer {
  id: string;
  name: string;
  state: string;
  image: string;
  stack?: string;
  ports?: TopoPort[];
}

export interface TopoPort {
  host_port: number;
  container_port: number;
  protocol: string;
}

export interface TopoLink {
  network_id: string;
  container_id: string;
  ipv4?: string;
  aliases?: string[];
}

export interface Topology {
  networks: TopoNetwork[];
  containers: TopoContainer[];
  links: TopoLink[];
}

export interface BackupTarget {
  id: number;
  name: string;
  type: string;
  config: any;
  status: string;
  total_bytes: number;
  used_bytes: number;
  free_bytes: number;
  last_checked_at?: string;
  created_at: string;
  updated_at: string;
}

export interface BackupSource {
  type: 'volume' | 'stack';
  name: string;
}

export interface BackupHook {
  container: string;
  cmd: string[];
}

export interface BackupJob {
  id: number;
  name: string;
  target_type: string;
  target_config: any;
  sources: BackupSource[];
  schedule: string;
  retention_count: number;
  retention_days: number;
  encrypt: boolean;
  pre_hooks: BackupHook[];
  post_hooks: BackupHook[];
  enabled: boolean;
  last_run_at?: string;
  next_run_at?: string;
  created_at: string;
  updated_at: string;
}

export interface BackupJobInput {
  name: string;
  target_type: string;
  target_config: any;
  sources: BackupSource[];
  schedule: string;
  retention_count: number;
  retention_days: number;
  encrypt: boolean;
  pre_hooks: BackupHook[];
  post_hooks: BackupHook[];
  enabled: boolean;
}

export interface BackupRun {
  id: number;
  job_id: number;
  job_name: string;
  status: string;
  started_at: string;
  finished_at?: string;
  size_bytes: number;
  target_path?: string;
  sha256?: string;
  encrypted: boolean;
  error?: string;
  sources: BackupSource[];
}

export interface NotificationChannel {
  id: number;
  type: string;
  name: string;
  config: any;
  enabled: boolean;
  created_at: string;
  updated_at: string;
}

export interface AlertRule {
  id: number;
  name: string;
  container_filter: string;
  metric: string;
  operator: string;
  threshold: number;
  duration_seconds: number;
  channel_ids: number[];
  enabled: boolean;
  severity: string;
  cooldown_seconds: number;
  muted_until?: string;
  // Built-in rules (P.11.5) ship with the server. UI disables delete,
  // keeps edit + enable/disable available so admins can tune thresholds
  // or silence the rule without removing the baseline coverage.
  builtin: boolean;
  firing_since?: string;
  last_triggered_at?: string;
  last_resolved_at?: string;
  created_at: string;
  updated_at: string;
}

export interface AlertRuleInput {
  name: string;
  container_filter: string;
  metric: string;
  operator: string;
  threshold: number;
  duration_seconds: number;
  channel_ids: number[];
  enabled: boolean;
  severity: string;
  cooldown_seconds: number;
  muted_until?: string;
}

export interface AlertHistoryEntry {
  id: number;
  rule_id: number;
  rule_name: string;
  container_name: string;
  status: string;
  message: string;
  value: number;
  threshold: number;
  occurred_at: string;
}

export interface OIDCProvider {
  id: number;
  slug: string;
  display_name: string;
  issuer_url: string;
  client_id: string;
  scopes: string;
  group_claim?: string;
  admin_group?: string;
  operator_group?: string;
  default_role: string;
  enabled: boolean;
  created_at: string;
  updated_at: string;
}

export interface OIDCProviderInput {
  slug: string;
  display_name: string;
  issuer_url: string;
  client_id: string;
  client_secret: string;
  scopes: string;
  group_claim?: string;
  admin_group?: string;
  operator_group?: string;
  default_role: string;
  enabled: boolean;
}

export interface UpdateHistoryEntry {
  id: number;
  container_name: string;
  image_ref: string;
  old_digest: string;
  new_digest: string;
  rollback_tag: string;
  applied_at: string;
  rolled_back_at?: string;
}

export class ApiError extends Error {
  status: number;
  constructor(message: string, status: number) {
    super(message);
    this.status = status;
  }
}

async function request<T>(path: string, init: RequestInit = {}): Promise<T> {
  const headers: Record<string, string> = {
    'Content-Type': 'application/json',
    ...(init.headers as Record<string, string> ?? {})
  };
  if (auth.accessToken) {
    headers['Authorization'] = `Bearer ${auth.accessToken}`;
  }

  let res = await fetch(`${BASE}${path}`, { ...init, headers });

  // On 401, try one refresh then retry.
  if (res.status === 401 && auth.refreshToken) {
    const ok = await auth.refresh();
    if (ok && auth.accessToken) {
      headers['Authorization'] = `Bearer ${auth.accessToken}`;
      res = await fetch(`${BASE}${path}`, { ...init, headers });
    }
  }

  if (!res.ok) {
    let msg = `${res.status} ${res.statusText}`;
    try {
      const body = await res.json();
      if (body.error) msg = body.error;
    } catch { /* ignore */ }
    throw new ApiError(msg, res.status);
  }
  if (res.status === 204) return undefined as T;
  return res.json() as Promise<T>;
}

export const api = {
  health: () => request<{ status: string; version: string; docker: boolean }>('/health'),

  auth: {
    login: (username: string, password: string) =>
      request<{
        access_token?: string;
        refresh_token?: string;
        user?: { id: string; username: string; role: string; mfa_enabled?: boolean };
        mfa_required?: boolean;
        mfa_token?: string;
      }>('/auth/login', { method: 'POST', body: JSON.stringify({ username, password }) }),
    verifyMFA: (mfaToken: string, code: string) =>
      request<{
        access_token: string;
        refresh_token: string;
        user: { id: string; username: string; role: string; mfa_enabled?: boolean };
      }>('/auth/mfa', { method: 'POST', body: JSON.stringify({ mfa_token: mfaToken, code }) }),
    logout: () => {
      const body = auth.refreshToken ? JSON.stringify({ refresh_token: auth.refreshToken }) : '{}';
      return request<void>('/auth/logout', { method: 'POST', body }).finally(() => auth.clear());
    }
  },

  mfa: {
    enrollStart: () =>
      request<{ secret: string; url: string; qr_data_url: string }>('/mfa/enroll/start', { method: 'POST' }),
    enrollVerify: (code: string) =>
      request<{ recovery_codes: string[] }>('/mfa/enroll/verify', {
        method: 'POST',
        body: JSON.stringify({ code })
      }),
    disable: () => request<void>('/mfa', { method: 'DELETE' }),
    reset: (userId: string) => request<void>(`/users/${userId}/mfa`, { method: 'DELETE' })
  },

  stacks: {
    list: () => request<StackListEntry[]>('/stacks'),
    get: (name: string) => request<{ name: string; compose: string; env: string }>(`/stacks/${encodeURIComponent(name)}`),
    create: (name: string, compose: string, env?: string) =>
      request<{ name: string }>('/stacks', { method: 'POST', body: JSON.stringify({ name, compose, env }) }),
    update: (name: string, compose: string, env?: string) =>
      request<{ name: string }>(`/stacks/${encodeURIComponent(name)}`, { method: 'PUT', body: JSON.stringify({ compose, env }) }),
    delete: (name: string) =>
      request<void>(`/stacks/${encodeURIComponent(name)}`, { method: 'DELETE' }),
    // Git source (P.11.11)
    getGitSource: (name: string) =>
      request<StackGitSource>(`/stacks/${encodeURIComponent(name)}/git`),
    configureGitSource: (name: string, input: StackGitSourceInput) =>
      request<{ source: StackGitSource; sync?: StackGitSyncResult; sync_error?: string }>(
        `/stacks/${encodeURIComponent(name)}/git`,
        { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(input) }
      ),
    deleteGitSource: (name: string) =>
      request<void>(`/stacks/${encodeURIComponent(name)}/git`, { method: 'DELETE' }),
    syncGitSource: (name: string) =>
      request<StackGitSyncResult>(`/stacks/${encodeURIComponent(name)}/git/sync`, { method: 'POST' }),
    deploy: (name: string, host = 'local') => {
      const qs = host && host !== 'local' ? '?host=' + encodeURIComponent(host) : '';
      return request<{ stack: string; services: Array<{ name: string; container_id: string; image: string }> }>(
        `/stacks/${encodeURIComponent(name)}/deploy${qs}`,
        { method: 'POST' }
      );
    },
    stop: (name: string, host = 'local') => {
      const qs = host && host !== 'local' ? '?host=' + encodeURIComponent(host) : '';
      return request<void>(`/stacks/${encodeURIComponent(name)}/stop${qs}`, { method: 'POST' });
    },
    status: (name: string, host = 'local') => {
      const qs = host && host !== 'local' ? '?host=' + encodeURIComponent(host) : '';
      return request<Array<{ service: string; container_id: string; state: string; status: string; image: string }>>(
        `/stacks/${encodeURIComponent(name)}/status${qs}`
      );
    },
    // Scaling (P.8)
    listScale: (name: string, host = 'local') => {
      const qs = host && host !== 'local' ? '?host=' + encodeURIComponent(host) : '';
      return request<Array<{ service: string; replicas: number }>>(`/stacks/${encodeURIComponent(name)}/scale${qs}`);
    },
    getScale: (name: string, service: string, host = 'local') => {
      const qs = host && host !== 'local' ? '?host=' + encodeURIComponent(host) : '';
      return request<ScaleCheck>(`/stacks/${encodeURIComponent(name)}/services/${encodeURIComponent(service)}/scale${qs}`);
    },
    scale: (name: string, service: string, replicas: number, force = false, host = 'local') => {
      const qs = host && host !== 'local' ? '?host=' + encodeURIComponent(host) : '';
      return request<ScaleResult>(
        `/stacks/${encodeURIComponent(name)}/services/${encodeURIComponent(service)}/scale${qs}`,
        { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ replicas, force }) }
      );
    },
    getScalingRules: (name: string) =>
      request<ScalingConfig>(`/stacks/${encodeURIComponent(name)}/scaling-rules`),
    setScalingRules: (name: string, config: ScalingConfig) =>
      request<ScalingConfig>(`/stacks/${encodeURIComponent(name)}/scaling-rules`, {
        method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(config)
      }),
    deleteScalingRules: (name: string) =>
      request<void>(`/stacks/${encodeURIComponent(name)}/scaling-rules`, { method: 'DELETE' })
  },

  migrations: {
    list: (limit = 100) => request<Migration[]>(`/migrations?limit=${limit}`),
    active: () => request<Migration[]>('/migrations/active'),
    get: (name: string, id: string) =>
      request<Migration>(`/stacks/${encodeURIComponent(name)}/migrate/${id}`),
    initiate: (name: string, targetHostId: string) =>
      request<Migration>(`/stacks/${encodeURIComponent(name)}/migrate`, {
        method: 'POST', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ target_host_id: targetHostId })
      }),
    preflight: (name: string, targetHostId: string) =>
      request<PreflightResult>(`/stacks/${encodeURIComponent(name)}/migrate/preflight`, {
        method: 'POST', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ target_host_id: targetHostId })
      }),
    rollback: (name: string, id: string) =>
      request<void>(`/stacks/${encodeURIComponent(name)}/migrate/${id}/rollback`, { method: 'POST' }),
    purgeSource: (name: string, id: string) =>
      request<void>(`/stacks/${encodeURIComponent(name)}/migrate/${id}/source`, { method: 'DELETE' })
  },

  drains: {
    plan: (hostId: string) =>
      request<DrainPlan>(`/hosts/${encodeURIComponent(hostId)}/drain/plan`, { method: 'POST' }),
    execute: (hostId: string) =>
      request<Drain>(`/hosts/${encodeURIComponent(hostId)}/drain/execute`, { method: 'POST' }),
    get: (hostId: string, drainId: string) =>
      request<Drain>(`/hosts/${encodeURIComponent(hostId)}/drain/${drainId}`),
    pause: (hostId: string, drainId: string) =>
      request<void>(`/hosts/${encodeURIComponent(hostId)}/drain/${drainId}/pause`, { method: 'POST' }),
    resume: (hostId: string, drainId: string) =>
      request<void>(`/hosts/${encodeURIComponent(hostId)}/drain/${drainId}/resume`, { method: 'POST' }),
    abort: (hostId: string, drainId: string) =>
      request<void>(`/hosts/${encodeURIComponent(hostId)}/drain/${drainId}/abort`, { method: 'POST' })
  },

  roles: {
    list: () => request<CustomRole[]>('/roles'),
    get: (name: string) => request<CustomRole>(`/roles/${encodeURIComponent(name)}`),
    permissions: () => request<PermissionInfo[]>('/roles/permissions'),
    create: (role: { name: string; display: string; permissions: string[] }) =>
      request<CustomRole>('/roles', {
        method: 'POST', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(role)
      }),
    update: (name: string, role: { display: string; permissions: string[] }) =>
      request<CustomRole>(`/roles/${encodeURIComponent(name)}`, {
        method: 'PUT', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(role)
      }),
    delete: (name: string) =>
      request<void>(`/roles/${encodeURIComponent(name)}`, { method: 'DELETE' })
  },

  apiTokens: {
    list: () => request<ApiToken[]>('/settings/api-tokens'),
    create: (input: { name: string; role: string; expires_in_days: number }) =>
      request<ApiToken & { token: string }>('/settings/api-tokens', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(input)
      }),
    revoke: (id: number) =>
      request<void>(`/settings/api-tokens/${id}`, { method: 'DELETE' })
  },

  registries: {
    list: () => request<Registry[]>('/settings/registries'),
    create: (input: RegistryInput) =>
      request<Registry>('/settings/registries', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(input)
      }),
    update: (id: number, input: RegistryInput) =>
      request<Registry>(`/settings/registries/${id}`, {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(input)
      }),
    delete: (id: number) =>
      request<void>(`/settings/registries/${id}`, { method: 'DELETE' }),
    test: (id: number) =>
      request<RegistryTestResult>(`/settings/registries/${id}/test`, { method: 'POST' })
  },

  globalEnv: {
    list: () => request<Array<{ id: number; key: string; value: string; group_name: string; encrypted: boolean; created_at: string; updated_at: string }>>('/global-env'),
    create: (data: { key: string; value: string; group_name: string }) =>
      request<any>('/global-env', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(data) }),
    update: (id: number, data: { key: string; value: string; group_name: string }) =>
      request<any>(`/global-env/${id}`, { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(data) }),
    delete: (id: number) => request<void>(`/global-env/${id}`, { method: 'DELETE' }),
    groups: () => request<string[]>('/global-env/groups')
  },

  templates: {
    list: () => request<StackTemplate[]>('/templates'),
    get: (id: number) => request<StackTemplate>(`/templates/${id}`),
    create: (input: StackTemplateInput) =>
      request<StackTemplate>('/templates', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(input)
      }),
    update: (id: number, input: StackTemplateInput) =>
      request<StackTemplate>(`/templates/${id}`, {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(input)
      }),
    delete: (id: number) => request<void>(`/templates/${id}`, { method: 'DELETE' }),
    deploy: (id: number, payload: { stack_name: string; host_id?: string; values?: Record<string, string> }) =>
      request<TemplateDeployResponse>(`/templates/${id}/deploy`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(payload)
      }),
    exportURL: (id: number) => `/api/v1/templates/${id}/export`
  },

  hosts: {
    list: () => request<HostInfo[]>('/hosts'),
    // Host tag management (P.11.2). Reads work for any authenticated
    // user; mutations require user.manage.
    listTags: (hostId: string) =>
      request<string[]>(`/hosts/${encodeURIComponent(hostId)}/tags`),
    setTags: (hostId: string, tags: string[]) =>
      request<string[]>(`/hosts/${encodeURIComponent(hostId)}/tags`, {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ tags })
      }),
    addTag: (hostId: string, tag: string) =>
      request<string[]>(`/hosts/${encodeURIComponent(hostId)}/tags`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ tag })
      }),
    removeTag: (hostId: string, tag: string) =>
      request<void>(`/hosts/${encodeURIComponent(hostId)}/tags/${encodeURIComponent(tag)}`, {
        method: 'DELETE'
      }),
    allTags: () => request<string[]>('/hosts/tags/all')
  },

  containers: {
    // Returns a bare array for single-host mode (including the default
    // "local" host). Returns a FanOutResponse wrapper when host='all',
    // in which case each row already has host_id + host_name attached
    // via backend struct embedding, and unreachable_hosts reports any
    // hosts that failed the fan-out.
    list: (all = false, host = 'local') => {
      const params = new URLSearchParams();
      if (all) params.set('all', 'true');
      if (host && host !== 'local') params.set('host', host);
      const qs = params.toString();
      return request<any[] | FanOutResponse<any>>(`/containers${qs ? '?' + qs : ''}`);
    },
    inspect: (id: string, host = 'local') => {
      const qs = host && host !== 'local' ? '?host=' + encodeURIComponent(host) : '';
      return request<any>(`/containers/${id}${qs}`);
    },
    _hostQuery: (host?: string) => (host && host !== 'local' ? '?host=' + encodeURIComponent(host) : ''),
    start: (id: string, host = 'local') => {
      const qs = host && host !== 'local' ? '?host=' + encodeURIComponent(host) : '';
      return request<void>(`/containers/${id}/start${qs}`, { method: 'POST' });
    },
    stop: (id: string, host = 'local') => {
      const qs = host && host !== 'local' ? '?host=' + encodeURIComponent(host) : '';
      return request<void>(`/containers/${id}/stop${qs}`, { method: 'POST' });
    },
    restart: (id: string, host = 'local') => {
      const qs = host && host !== 'local' ? '?host=' + encodeURIComponent(host) : '';
      return request<void>(`/containers/${id}/restart${qs}`, { method: 'POST' });
    },
    pause: (id: string, host = 'local') => {
      const qs = host && host !== 'local' ? '?host=' + encodeURIComponent(host) : '';
      return request<void>(`/containers/${id}/pause${qs}`, { method: 'POST' });
    },
    unpause: (id: string, host = 'local') => {
      const qs = host && host !== 'local' ? '?host=' + encodeURIComponent(host) : '';
      return request<void>(`/containers/${id}/unpause${qs}`, { method: 'POST' });
    },
    kill: (id: string, signal = '', host = 'local') => {
      const qs = host && host !== 'local' ? '?host=' + encodeURIComponent(host) : '';
      return request<void>(`/containers/${id}/kill${qs}`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ signal })
      });
    },
    remove: (id: string, force = false, host = 'local') => {
      const params = new URLSearchParams();
      if (force) params.set('force', 'true');
      if (host && host !== 'local') params.set('host', host);
      const qs = params.toString();
      return request<void>(`/containers/${id}${qs ? '?' + qs : ''}`, { method: 'DELETE' });
    },
    updateInfo: (id: string) =>
      request<UpdatePreview>(`/containers/${id}/update-info`),
    doUpdate: (id: string) =>
      request<UpdateResult>(`/containers/${id}/update`, { method: 'POST' }),
    rollback: (id: string, historyId: number) =>
      request<UpdateResult>(`/containers/${id}/rollback`, {
        method: 'POST',
        body: JSON.stringify({ history_id: historyId })
      }),
    updateHistory: (id: string) =>
      request<UpdateHistoryEntry[]>(`/containers/${id}/update-history`),
    metrics: (id: string, from: number, to: number, resolution: 'raw' | '1m' | '1h' = 'raw') =>
      request<MetricsSample[]>(
        `/containers/${id}/metrics?from=${from}&to=${to}&resolution=${resolution}`
      )
  },

  images: {
    // Bare array for single-host, FanOutResponse for host='all'.
    list: (all = false, host = 'local') => {
      const params = new URLSearchParams();
      if (all) params.set('all', 'true');
      if (host && host !== 'local') params.set('host', host);
      const qs = params.toString();
      return request<any[] | FanOutResponse<any>>(`/images${qs ? '?' + qs : ''}`);
    },
    pull: (image: string) => request<any>('/images/pull', { method: 'POST', body: JSON.stringify({ image }) }),
    remove: (id: string, force = false) =>
      request<any>(`/images/${encodeURIComponent(id)}${force ? '?force=true' : ''}`, { method: 'DELETE' }),
    prune: () => request<{ ImagesDeleted: any[]; SpaceReclaimed: number }>('/images/prune', { method: 'POST' }),
    scan: (id: string) =>
      request<ScanReport>(`/images/${encodeURIComponent(id)}/scan`, { method: 'POST' }),
    getScan: (id: string) =>
      request<ScanReport>(`/images/${encodeURIComponent(id)}/scan`)
  },

  networks: {
    list: (host = 'local') => {
      const qs = host && host !== 'local' ? '?host=' + encodeURIComponent(host) : '';
      return request<any[] | FanOutResponse<any>>(`/networks${qs}`);
    },
    inspect: (id: string, host = 'local') => {
      const qs = host && host !== 'local' ? '?host=' + encodeURIComponent(host) : '';
      return request<any>(`/networks/${id}${qs}`);
    },
    create: (name: string, driver?: string) =>
      request<{ Id: string }>('/networks', { method: 'POST', body: JSON.stringify({ name, driver }) }),
    remove: (id: string) => request<void>(`/networks/${id}`, { method: 'DELETE' }),
    prune: () => request<any>('/networks/prune', { method: 'POST' }),
    topology: () => request<Topology>('/networks/topology')
  },

  volumes: {
    list: (host = 'local') => {
      const qs = host && host !== 'local' ? '?host=' + encodeURIComponent(host) : '';
      return request<any[] | FanOutResponse<any>>(`/volumes${qs}`);
    },
    inspect: (name: string, host = 'local') => {
      const qs = host && host !== 'local' ? '?host=' + encodeURIComponent(host) : '';
      return request<any>(`/volumes/${encodeURIComponent(name)}${qs}`);
    },
    create: (name: string, driver?: string) =>
      request<any>('/volumes', { method: 'POST', body: JSON.stringify({ name, driver }) }),
    remove: (name: string, force = false) =>
      request<void>(`/volumes/${encodeURIComponent(name)}${force ? '?force=true' : ''}`, { method: 'DELETE' }),
    prune: () => request<any>('/volumes/prune', { method: 'POST' }),
    // P.11.8 — volume content browsing. Admin-only; every call is audited.
    browse: (name: string, path = '', host = 'local') => {
      const qs = new URLSearchParams();
      if (path) qs.set('path', path);
      if (host && host !== 'local') qs.set('host', host);
      const suffix = qs.toString() ? `?${qs.toString()}` : '';
      return request<VolumeEntry[]>(`/volumes/${encodeURIComponent(name)}/browse${suffix}`);
    },
    readFile: (name: string, path: string, host = 'local') => {
      const qs = new URLSearchParams({ path });
      if (host && host !== 'local') qs.set('host', host);
      return request<VolumeFileResult>(`/volumes/${encodeURIComponent(name)}/browse/file?${qs.toString()}`);
    }
  },

  backups: {
    listTargets: () => request<BackupTarget[]>('/backups/targets'),
    createTarget: (input: { name: string; type: string; config: any }) =>
      request<BackupTarget>('/backups/targets', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(input) }),
    updateTarget: (id: number, input: { name: string; type: string; config: any }) =>
      request<BackupTarget>(`/backups/targets/${id}`, { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(input) }),
    deleteTarget: (id: number) => request<void>(`/backups/targets/${id}`, { method: 'DELETE' }),
    testTarget: (id: number) => request<{ status: string; total_bytes: number; used_bytes: number; free_bytes: number; error?: string }>(`/backups/targets/${id}/test`, { method: 'POST' }),
    testTargetConfig: (type: string, config: any) =>
      request<{ status: string; total_bytes: number; used_bytes: number; free_bytes: number; error?: string }>('/backups/targets/test-config', {
        method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ name: 'test', type, config })
      }),
    discoverShares: (host: string, port: number, username: string, password: string) =>
      request<{ shares: string[]; error?: string }>('/backups/targets/discover-shares', {
        method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ host, port: port || 445, username, password })
      }),
    listJobs: () => request<BackupJob[]>('/backups/jobs'),
    getJob: (id: number) => request<BackupJob>(`/backups/jobs/${id}`),
    createJob: (input: BackupJobInput) =>
      request<BackupJob>('/backups/jobs', { method: 'POST', body: JSON.stringify(input) }),
    updateJob: (id: number, input: BackupJobInput) =>
      request<BackupJob>(`/backups/jobs/${id}`, { method: 'PUT', body: JSON.stringify(input) }),
    deleteJob: (id: number) => request<void>(`/backups/jobs/${id}`, { method: 'DELETE' }),
    runJob: (id: number) => request<BackupRun>(`/backups/jobs/${id}/run`, { method: 'POST' }),
    listRuns: (limit = 100) => request<BackupRun[]>(`/backups/runs?limit=${limit}`),
    restore: (runId: number, destVolume: string) =>
      request<void>(`/backups/runs/${runId}/restore`, {
        method: 'POST',
        body: JSON.stringify({ dest_volume: destVolume })
      })
  },

  users: {
    me: () => request<{ id: string; username: string; email?: string; role: string; scope_tags?: string[] }>('/me'),
    list: () => request<Array<{ id: string; username: string; email?: string; role: string; scope_tags?: string[] }>>('/users'),
    create: (username: string, password: string, role: string, email?: string) =>
      request<{ id: string; username: string; role: string }>('/users', {
        method: 'POST',
        body: JSON.stringify({ username, password, role, email })
      }),
    update: (id: string, email: string, role: string, scope_tags?: string[]) =>
      request<{ id: string; username: string; role: string; scope_tags?: string[] }>(`/users/${id}`, {
        method: 'PUT',
        body: JSON.stringify({ email, role, scope_tags: scope_tags ?? [] })
      }),
    delete: (id: string) => request<void>(`/users/${id}`, { method: 'DELETE' }),
    changePassword: (id: string, password: string) =>
      request<void>(`/users/${id}/password`, {
        method: 'PUT',
        body: JSON.stringify({ password })
      })
  },

  system: {
    // host='local' (default) → bare Metrics object for the central server.
    // host='<id>'            → bare Metrics object for that specific agent.
    // host='all'             → FanOutResponse with one row per online host.
    // The frontend uses isFanOut() to narrow the return type at the caller.
    metrics: (host = 'local') => {
      const qs = host && host !== 'local' ? '?host=' + encodeURIComponent(host) : '';
      return request<SystemMetrics | FanOutResponse<SystemMetrics>>(`/system/metrics${qs}`);
    },
    // Default-system-backup status for the sidebar pill + settings.
    backupStatus: () => request<BackupStatus>('/system/backup-status'),
    info: () => request<{ version: string; commit: string; build_date: string; go_version: string; os: string; arch: string; uptime_seconds: number }>('/system/info'),
    settings: () => request<Array<{ key: string; value: string }>>('/settings'),
    updateSettings: (entries: Array<{ key: string; value: string }>) =>
      request<Array<{ key: string; value: string }>>('/settings', {
        method: 'PUT', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(entries)
      }),
    // Toggle the auto-created daily system backup job.
    setBackupEnabled: (enabled: boolean) =>
      request<BackupStatus>('/backups/system/enabled', {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ enabled })
      })
  },

  audit: {
    list: (limit = 100, action = '', userId = '') => {
      const params = new URLSearchParams();
      params.set('limit', String(limit));
      if (action) params.set('action', action);
      if (userId) params.set('user_id', userId);
      return request<Array<{ id: number; ts: string; user_id?: string; username?: string; action: string; target?: string; details?: string; prev_hash?: string; row_hash?: string }>>(
        `/audit?${params}`
      );
    },
    verify: () =>
      request<{
        verified: number;
        broken: number;
        first_break?: number;
        break_reason?: string;
        genesis: string;
        warnings?: string[];
      }>('/audit/verify'),
    getRetention: () =>
      request<{ config: AuditRetentionConfig; preview: AuditRetentionPreview }>('/audit/retention'),
    setRetention: (cfg: AuditRetentionConfig) =>
      request<{ config: AuditRetentionConfig; preview: AuditRetentionPreview }>('/audit/retention', {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(cfg)
      }),
    runRetention: () =>
      request<AuditRetentionResult>('/audit/retention/run', { method: 'POST' }),
    getWebhook: () => request<AuditWebhookConfig>('/audit/webhook'),
    setWebhook: (cfg: AuditWebhookInput) =>
      request<AuditWebhookConfig>('/audit/webhook', {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(cfg)
      }),
    testWebhook: () =>
      request<{ status: string }>('/audit/webhook/test', { method: 'POST' })
  },

  convert: {
    runToCompose: (command: string) =>
      request<{ yaml: string; warnings?: string[] }>('/convert/run-to-compose', {
        method: 'POST',
        body: JSON.stringify({ command })
      })
  },

  alerts: {
    listChannels: () => request<NotificationChannel[]>('/notifications/channels'),
    createChannel: (input: { type: string; name: string; config: any; enabled: boolean }) =>
      request<NotificationChannel>('/notifications/channels', {
        method: 'POST',
        body: JSON.stringify({ ...input, config: typeof input.config === 'string' ? JSON.parse(input.config) : input.config })
      }),
    updateChannel: (id: number, input: { type: string; name: string; config: any; enabled: boolean }) =>
      request<NotificationChannel>(`/notifications/channels/${id}`, {
        method: 'PUT',
        body: JSON.stringify({ ...input, config: typeof input.config === 'string' ? JSON.parse(input.config) : input.config })
      }),
    deleteChannel: (id: number) =>
      request<void>(`/notifications/channels/${id}`, { method: 'DELETE' }),
    testChannel: (id: number) =>
      request<void>(`/notifications/channels/${id}/test`, { method: 'POST' }),

    listRules: () => request<AlertRule[]>('/alerts/rules'),
    createRule: (input: AlertRuleInput) =>
      request<AlertRule>('/alerts/rules', { method: 'POST', body: JSON.stringify(input) }),
    updateRule: (id: number, input: AlertRuleInput) =>
      request<AlertRule>(`/alerts/rules/${id}`, { method: 'PUT', body: JSON.stringify(input) }),
    deleteRule: (id: number) => request<void>(`/alerts/rules/${id}`, { method: 'DELETE' }),

    history: (limit = 100) => request<AlertHistoryEntry[]>(`/alerts/history?limit=${limit}`)
  },

  oidc: {
    listPublic: () =>
      request<Array<{ slug: string; display_name: string }>>('/auth/oidc/providers'),
    listAdmin: () =>
      request<Array<OIDCProvider>>('/oidc/providers'),
    create: (input: OIDCProviderInput) =>
      request<OIDCProvider>('/oidc/providers', { method: 'POST', body: JSON.stringify(input) }),
    update: (id: number, input: OIDCProviderInput) =>
      request<OIDCProvider>(`/oidc/providers/${id}`, { method: 'PUT', body: JSON.stringify(input) }),
    delete: (id: number) =>
      request<void>(`/oidc/providers/${id}`, { method: 'DELETE' })
  },

  agents: {
    list: () => request<Agent[]>('/agents'),
    get: (id: string) => request<Agent>(`/agents/${id}`),
    create: (name: string) =>
      request<AgentCreateResult>('/agents', { method: 'POST', body: JSON.stringify({ name }) }),
    delete: (id: string) => request<void>(`/agents/${id}`, { method: 'DELETE' }),
    upgrade: (id: string) =>
      request<{ status: string; version: string }>(`/agents/${id}/upgrade`, { method: 'POST' }),
    // P.11.16 upgrade policy
    getUpgradePolicy: () => request<AgentUpgradePolicy>('/agents/upgrade-policy'),
    setUpgradePolicy: (input: AgentUpgradeInput) =>
      request<AgentUpgradePolicy>('/agents/upgrade-policy', {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(input)
      }),
    runUpgradePolicy: () =>
      request<AgentUpgradePolicy>('/agents/upgrade-policy/run', { method: 'POST' })
  },

  proxy: {
    status: () =>
      request<{ enabled: boolean; running: boolean; admin_ok: boolean; version?: string; container?: string }>('/proxy/status'),
    enable: () => request<any>('/proxy/enable', { method: 'POST' }),
    disable: () => request<void>('/proxy/disable', { method: 'POST' }),
    // tls_mode is narrowed to its three valid values so the proxy page's
    // local ProxyRoute interface can assign the response directly without
    // casting. The backend validates the mode on write anyway.
    listRoutes: () =>
      request<Array<{ id: number; host: string; upstream: string; tls_mode: 'auto' | 'internal' | 'none'; created_at: string; updated_at: string }>>('/proxy/routes'),
    createRoute: (host: string, upstream: string, tls_mode: string) =>
      request<any>('/proxy/routes', { method: 'POST', body: JSON.stringify({ host, upstream, tls_mode }) }),
    updateRoute: (id: number, upstream: string, tls_mode: string) =>
      request<void>(`/proxy/routes/${id}`, { method: 'PUT', body: JSON.stringify({ upstream, tls_mode }) }),
    deleteRoute: (id: number) => request<void>(`/proxy/routes/${id}`, { method: 'DELETE' })
  },

  ws: {
    ticket: () => request<{ ticket: string }>('/ws/ticket', { method: 'POST' })
  }
};
