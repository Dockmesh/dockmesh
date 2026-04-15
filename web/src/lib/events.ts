// Reconnecting event-stream helper for /ws/events subscriptions.
//
// The Dockmesh UI has several pages that listen for live Docker + stacks
// events over a single shared WebSocket. Before this helper, each page
// handled WS errors by just flipping `live = false` — if the backend
// restarted, the network flapped, or the ticket expired, the stream went
// dead until the user manually reloaded. This class wraps the connect
// loop with exponential backoff reconnect + a status callback so the UI
// can render a "reconnecting…" indicator.
//
// Lifetime: create once per page, call `start()` from a `$effect`, and
// return `stop` as the effect cleanup. The helper owns the timer and
// socket; callers only react to `onMessage` and `onStatus`.
//
// This is deliberately a plain class (not a rune) so it can live outside
// a `.svelte` component and be covered by a unit test later.

import { api } from './api';

export type ConnStatus = 'connecting' | 'live' | 'reconnecting' | 'closed';

export interface EventStreamOptions {
  onMessage: (msg: any) => void;
  onStatus?: (status: ConnStatus) => void;
  // Backoff schedule in milliseconds; loops on the last value.
  backoff?: number[];
}

const DEFAULT_BACKOFF = [1000, 2000, 5000, 10000, 30000];

export class EventStream {
  private ws: WebSocket | null = null;
  private reconnectTimer: ReturnType<typeof setTimeout> | null = null;
  private attempt = 0;
  private stopped = false;
  private readonly backoff: number[];

  constructor(private readonly opts: EventStreamOptions) {
    this.backoff = opts.backoff ?? DEFAULT_BACKOFF;
  }

  start() {
    this.stopped = false;
    void this.connect();
  }

  stop = () => {
    this.stopped = true;
    if (this.reconnectTimer) {
      clearTimeout(this.reconnectTimer);
      this.reconnectTimer = null;
    }
    if (this.ws) {
      // Drop handlers first so the onclose we're about to trigger
      // doesn't schedule another reconnect.
      this.ws.onopen = null;
      this.ws.onmessage = null;
      this.ws.onclose = null;
      this.ws.onerror = null;
      try { this.ws.close(); } catch { /* ignore */ }
      this.ws = null;
    }
    this.opts.onStatus?.('closed');
  };

  private setStatus(s: ConnStatus) {
    this.opts.onStatus?.(s);
  }

  private async connect() {
    if (this.stopped) return;
    this.setStatus(this.attempt === 0 ? 'connecting' : 'reconnecting');

    let ticket: string;
    try {
      const res = await api.ws.ticket();
      ticket = res.ticket;
    } catch {
      this.scheduleReconnect();
      return;
    }

    if (this.stopped) return;

    const proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
    const url = `${proto}//${location.host}/api/v1/ws/events?ticket=${ticket}`;
    try {
      this.ws = new WebSocket(url);
    } catch {
      this.scheduleReconnect();
      return;
    }

    this.ws.onopen = () => {
      this.attempt = 0;
      this.setStatus('live');
    };
    this.ws.onmessage = (ev) => {
      try {
        const msg = JSON.parse(ev.data);
        this.opts.onMessage(msg);
      } catch { /* ignore malformed frames */ }
    };
    this.ws.onerror = () => {
      // Let onclose handle the reconnect so we don't do it twice.
    };
    this.ws.onclose = () => {
      this.ws = null;
      if (this.stopped) return;
      this.scheduleReconnect();
    };
  }

  private scheduleReconnect() {
    if (this.stopped) return;
    const delay = this.backoff[Math.min(this.attempt, this.backoff.length - 1)];
    this.attempt++;
    this.setStatus('reconnecting');
    this.reconnectTimer = setTimeout(() => {
      this.reconnectTimer = null;
      void this.connect();
    }, delay);
  }
}
