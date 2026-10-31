// Small DOM helpers shared by dialogs, drawers and menus.

const FOCUSABLE =
  'a[href], button:not([disabled]), input:not([disabled]):not([type="hidden"]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])';

export function focusables(root: HTMLElement): HTMLElement[] {
  return [...root.querySelectorAll<HTMLElement>(FOCUSABLE)].filter(
    (el) => el.offsetParent !== null || el === document.activeElement
  );
}

// Keeps Tab inside the node, focuses the first field on mount and gives
// focus back to whatever had it before the node opened.
export function trapFocus(node: HTMLElement) {
  const before = document.activeElement as HTMLElement | null;
  queueMicrotask(() => {
    if (node.contains(document.activeElement)) return;
    const first =
      node.querySelector<HTMLElement>('[data-autofocus]') ??
      node.querySelector<HTMLElement>('input:not([type="hidden"]):not([disabled]), select, textarea') ??
      focusables(node)[0];
    (first ?? node).focus();
  });
  function onKey(e: KeyboardEvent) {
    if (e.key !== 'Tab') return;
    const list = focusables(node);
    if (list.length === 0) {
      e.preventDefault();
      return;
    }
    const first = list[0];
    const last = list[list.length - 1];
    if (e.shiftKey && document.activeElement === first) {
      e.preventDefault();
      last.focus();
    } else if (!e.shiftKey && document.activeElement === last) {
      e.preventDefault();
      first.focus();
    }
  }
  node.addEventListener('keydown', onKey);
  return {
    destroy() {
      node.removeEventListener('keydown', onKey);
      if (before && document.contains(before)) before.focus();
    }
  };
}

// Moves the node to <body> so `position: fixed` is relative to the viewport
// even when an ancestor creates a containing block (transform, filter).
export function portal(node: HTMLElement) {
  document.body.appendChild(node);
  return {
    destroy() {
      node.remove();
    }
  };
}
