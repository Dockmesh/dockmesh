// Live view of the server's long-running operations (image updates,
// backup runs, restores, new proxy sites). One store for the whole app:
// the top-bar indicator, the progress dialog and inline row progress all
// read from it, so leaving a page never loses track of running work.
//
// Updates arrive on /ws/events ("source":"operations"); while the socket
// is down the store polls the list endpoint instead.

import { api, type BackupRun, type Operation, type OperationLogLine } from '$lib/api';
import { EventStream, type ConnStatus } from '$lib/events';
import { auth } from './auth.svelte';
import { toast } from './toast.svelte';

const LOG_CAP = 500;
const POLL_LIVE_MS = 30_000;
const POLL_OFFLINE_MS = 4_000;
// Finished operations stay in the indicator's "recent" list this long.
const RECENT_MS = 30 * 60_000;

export { kindLabel, isActive, currentStep, overallProgress, opRef, hasWarning, failedStep, elapsed } from '$lib/operationsProgress';
import { kindLabel, isActive, opRef, failedStep, shortMessage } from '$lib/operationsProgress';

function createOperationsStore() {
  let byId = $state<Record<string, Operation>>({});
  let logs = $state<Record<string, OperationLogLine[]>>({});
  let conn = $state<ConnStatus>('closed');
  let dialogId = $state<string | null>(null);
  let stream: EventStream | null = null;
  let pollTimer: ReturnType<typeof setTimeout> | null = null;
  let started = false;
  // Ids whose finish this tab already announced.
  const announced = new Set<string>();

  function upsert(op: Operation) {
    const prev = byId[op.id];
    if (prev && prev.status !== op.status && !isActive(op)) announce(op);
    else if (!prev && !isActive(op) && Date.now() - Date.parse(op.finished_at ?? op.created_at) < 15_000) announce(op);
    byId[op.id] = op;
  }

  // A toast for work this user started; others' work only shows in the
  // indicator.
  function announce(op: Operation) {
    if (announced.has(op.id)) return;
    announced.add(op.id);
    if (op.actor && op.actor !== auth.user?.id) return;
    const what = `${kindLabel(op.kind)} ${op.target}`;
    if (op.status === 'succeeded') {
      const warned = op.steps.find((s) => s.status === 'warning');
      if (warned) toast.warning(`${what} finished with a warning`, shortMessage(warned.detail));
      else toast.success(`${what} finished`);
    } else if (op.status === 'failed') {
      const failed = failedStep(op);
      toast.error(`${what} failed${failed ? ` at “${failed.label}”` : ''}`, shortMessage(op.error));
    } else if (op.status === 'cancelled') {
      toast.info(`${what} cancelled`);
    }
  }

  function appendLog(id: string, line: OperationLogLine) {
    const cur = logs[id];
    if (!cur) return;
    const next = cur.length >= LOG_CAP ? cur.slice(cur.length - LOG_CAP + 1) : cur.slice();
    next.push(line);
    logs[id] = next;
  }

  async function poll() {
    pollTimer = null;
    if (!auth.isAuthenticated) return;
    try {
      const list = await api.operations.list({ limit: 50 });
      for (const op of list) upsert(op);
      // Drawer log follows the poll while the socket is down.
      if (dialogId && conn !== 'live' && byId[dialogId] && isActive(byId[dialogId])) await load(dialogId);
    } catch {
      /* next poll retries */
    }
    schedule();
  }

  function schedule() {
    if (!started) return;
    if (pollTimer) clearTimeout(pollTimer);
    const anyActive = Object.values(byId).some(isActive);
    const ms = conn === 'live' ? POLL_LIVE_MS : anyActive ? POLL_OFFLINE_MS : POLL_OFFLINE_MS * 4;
    pollTimer = setTimeout(poll, ms);
  }

  async function load(id: string): Promise<Operation | null> {
    try {
      const op = await api.operations.get(id);
      logs[id] = op.log ?? [];
      const { log: _log, ...rest } = op;
      upsert(rest as Operation);
      return op;
    } catch {
      return null;
    }
  }

  return {
    get all(): Operation[] {
      return Object.values(byId).sort((a, b) => b.created_at.localeCompare(a.created_at));
    },
    get active(): Operation[] {
      return this.all.filter(isActive);
    },
    get recent(): Operation[] {
      const now = Date.now();
      return this.all.filter((o) => !isActive(o) && now - Date.parse(o.finished_at ?? o.created_at) < RECENT_MS);
    },
    get live() {
      return conn === 'live';
    },
    get dialogId() {
      return dialogId;
    },
    get(id: string | null | undefined): Operation | undefined {
      return id ? byId[id] : undefined;
    },
    logOf(id: string): OperationLogLine[] {
      return logs[id] ?? [];
    },
    // Newest operation for ref (e.g. "backup-run:42", "container:<id>").
    latest(ref: string, kind?: string): Operation | undefined {
      return this.all.find((o) => o.ref === ref && (!kind || o.kind === kind));
    },
    // The operation of a backup run, once the store knows it.
    backupRun(runId?: number | null): Operation | undefined {
      return runId ? this.latest(opRef.backupRun(runId), 'backup.run') : undefined;
    },
    // Starts a backup job; `op` resolves once its operation shows up.
    async startBackup(jobId: number): Promise<{ run: BackupRun; op: Promise<Operation | undefined> }> {
      const since = Date.now();
      const run = await api.backups.runJob(jobId);
      return { run, op: this.find(opRef.backupRun(run.id), 'backup.run', since) };
    },
    start() {
      if (started || !auth.isAuthenticated) return;
      started = true;
      stream = new EventStream({
        onMessage: (msg) => {
          if (msg?.source !== 'operations' || !msg.op) return;
          upsert(msg.op as Operation);
          if (msg.type === 'log' && msg.line) appendLog(msg.op.id, msg.line as OperationLogLine);
        },
        onStatus: (s) => {
          const was = conn;
          conn = s;
          // Catch up on what happened while the socket was away.
          if (s === 'live' && was !== 'live') void poll();
          else schedule();
        }
      });
      stream.start();
      void poll();
    },
    stop() {
      started = false;
      stream?.stop();
      stream = null;
      if (pollTimer) clearTimeout(pollTimer);
      pollTimer = null;
      byId = {};
      logs = {};
      dialogId = null;
    },
    // Adds operations a request just returned, before the socket reports them.
    track(ops: Operation | Operation[]) {
      for (const op of Array.isArray(ops) ? ops : [ops]) upsert(op);
      schedule();
    },
    // Waits briefly for the operation of ref to show up, e.g. right after
    // starting an action whose response does not name its operation.
    async find(ref: string, kind: string, since: number, timeoutMs = 4000): Promise<Operation | undefined> {
      const deadline = Date.now() + timeoutMs;
      // Server and browser clocks may differ, so a minute of slack.
      const fresh = () =>
        this.all.find(
          (o) => o.ref === ref && o.kind === kind && (isActive(o) || Date.parse(o.created_at) >= since - 60_000)
        );
      while (Date.now() < deadline) {
        const hit = fresh();
        if (hit) return hit;
        try {
          for (const op of await api.operations.list({ ref, kind, limit: 5 })) upsert(op);
        } catch {
          /* retry until the deadline */
        }
        const again = fresh();
        if (again) return again;
        await new Promise((r) => setTimeout(r, 500));
      }
      return undefined;
    },
    async open(id: string) {
      dialogId = id;
      await load(id);
    },
    close() {
      dialogId = null;
    },
    load
  };
}

export const operations = createOperationsStore();
