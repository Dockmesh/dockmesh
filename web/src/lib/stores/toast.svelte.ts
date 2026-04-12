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
    error: (title: string, description?: string) => push('error', title, description, 6000),
    info: (title: string, description?: string) => push('info', title, description),
    warning: (title: string, description?: string) => push('warning', title, description),
    dismiss
  };
}

export const toast = createToastStore();
