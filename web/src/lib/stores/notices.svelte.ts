import { api, type Notice } from '$lib/api';

// Shared by the banner, the overview and the Activity page so all show the
// same set and the server is polled once.
function createNoticeStore() {
  let items = $state<Notice[]>([]);

  async function refresh() {
    try {
      items = await api.notices.list();
    } catch {
      /* keep the last known set on poll failures */
    }
  }

  async function dismiss(n: Notice) {
    await api.notices.dismiss(n.id);
    items = items.map((x) => (x.id === n.id ? { ...x, dismissed: true } : x));
  }

  return {
    get items() { return items; },
    get banner() { return items.filter((n) => !n.dismissed); },
    refresh,
    dismiss,
    clear() { items = []; },
  };
}

export const notices = createNoticeStore();
