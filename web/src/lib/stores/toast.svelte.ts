export type ToastVariant = 'success' | 'error' | 'info' | 'warning';

export interface Toast {
  id: number;
  variant: ToastVariant;
  title: string;
  description?: string;
  duration: number;
}

// Default auto-dismiss timings (ms) — middle ground of the industry
// standards (Material 4-10s, GitHub 6s, Sonner 4s). Severity scales the
// dwell-time so the user has more time to read what just blew up.
const DEFAULTS = {
  success: 4000,
  info:    5000,
  warning: 7000,
  error:  10000,
};

// Cap concurrent visible toasts so a flood (e.g. a chain of failed
// background polls) can't blanket the bottom-right corner. Anything
// older than the cap is silently dismissed; the Notification Center
// is the durable record for important events.
const MAX_VISIBLE = 5;

function createToastStore() {
  let toasts = $state<Toast[]>([]);
  let nextId = 1;
  // Per-toast dismiss-timer handles so hover-pause can clear+reset.
  const timers = new Map<number, ReturnType<typeof setTimeout>>();

  function scheduleDismiss(id: number, ms: number) {
    if (ms <= 0) return;
    const t = setTimeout(() => {
      timers.delete(id);
      dismiss(id);
    }, ms);
    timers.set(id, t);
  }

  function push(variant: ToastVariant, title: string, description?: string, duration?: number) {
    const id = nextId++;
    const dur = duration ?? DEFAULTS[variant];
    const t: Toast = { id, variant, title, description, duration: dur };
    let next = [...toasts, t];
    // Evict oldest if we just blew past the visible cap.
    while (next.length > MAX_VISIBLE) {
      const oldest = next.shift();
      if (oldest) {
        const timer = timers.get(oldest.id);
        if (timer) {
          clearTimeout(timer);
          timers.delete(oldest.id);
        }
      }
    }
    toasts = next;
    scheduleDismiss(id, dur);
    return id;
  }

  function dismiss(id: number) {
    const timer = timers.get(id);
    if (timer) {
      clearTimeout(timer);
      timers.delete(id);
    }
    toasts = toasts.filter((t) => t.id !== id);
  }

  // Hover-pause: when the cursor enters a toast we clear its timer so
  // the user can read long titles without them whisking away. On leave
  // the timer is restarted from full duration (deliberate — partial
  // resume confuses users who hover briefly to read then look away).
  function pause(id: number) {
    const timer = timers.get(id);
    if (timer) {
      clearTimeout(timer);
      timers.delete(id);
    }
  }

  function resume(id: number) {
    const t = toasts.find((x) => x.id === id);
    if (!t) return;
    if (timers.has(id)) return;
    scheduleDismiss(id, t.duration);
  }

  return {
    get items() {
      return toasts;
    },
    success: (title: string, description?: string, duration?: number) =>
      push('success', title, description, duration),
    info: (title: string, description?: string, duration?: number) =>
      push('info', title, description, duration),
    warning: (title: string, description?: string, duration?: number) =>
      push('warning', title, description, duration),
    // Errors auto-dismiss after 10s — long enough to read, short enough
    // that a flood doesn't bury the UI. For truly critical errors that
    // MUST be acknowledged, pass duration=0 explicitly OR rely on the
    // Notification Center (NotifCenter) where the same event lands
    // permanently.
    error: (title: string, description?: string, duration?: number) =>
      push('error', title, description, duration),
    dismiss,
    pause,
    resume
  };
}

export const toast = createToastStore();
