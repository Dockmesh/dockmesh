export type ToastVariant = 'success' | 'error' | 'info' | 'warning';

export interface Toast {
  id: number;
  variant: ToastVariant;
  title: string;
  description?: string;
  duration: number;
}

function createToastStore() {
  let toasts = $state<Toast[]>([]);
  let nextId = 1;

  function push(variant: ToastVariant, title: string, description?: string, duration = 4000) {
    const id = nextId++;
    const t: Toast = { id, variant, title, description, duration };
    toasts = [...toasts, t];
    if (duration > 0) {
      setTimeout(() => dismiss(id), duration);
    }
    return id;
  }

  function dismiss(id: number) {
    toasts = toasts.filter((t) => t.id !== id);
  }

  return {
    get items() {
      return toasts;
    },
    success: (title: string, description?: string) => push('success', title, description),
    // Error toasts stay on screen until the user dismisses them. A 4-6s
    // auto-hide is fine for "saved" confirmations but loses the signal
    // for real failures — users kept missing backend errors that
    // disappeared before they could read them. Pass a positive duration
    // explicitly if you need an auto-hiding error (rare; most cases
    // should stay sticky).
    error: (title: string, description?: string, duration = 0) => push('error', title, description, duration),
    info: (title: string, description?: string) => push('info', title, description),
    warning: (title: string, description?: string) => push('warning', title, description, 8000),
    dismiss
  };
}

export const toast = createToastStore();
