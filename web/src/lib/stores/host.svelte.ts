// Selected host for the multi-host views (slice 3.1.2). The selection is
// persisted in localStorage so navigating between pages doesn't reset it,
// and the available list is refreshed on demand from /api/v1/hosts.

import type { HostInfo } from '$lib/api';

const STORAGE_KEY = 'dockmesh_selected_host';

function loadInitial(): string {
  if (typeof window === 'undefined') return 'local';
  return window.localStorage.getItem(STORAGE_KEY) || 'local';
}

function createHostStore() {
  let selectedId = $state<string>(loadInitial());
  let available = $state<HostInfo[]>([]);

  function set(id: string) {
    selectedId = id;
    if (typeof window !== 'undefined') {
      window.localStorage.setItem(STORAGE_KEY, id);
    }
  }

  function setAvailable(list: HostInfo[]) {
    available = list;
    // If the previously selected host has gone offline / been removed,
    // fall back to local rather than firing failed API requests.
    if (selectedId !== 'local' && !list.some((h) => h.id === selectedId && h.status === 'online')) {
      set('local');
    }
  }

  return {
    get id() {
      return selectedId;
    },
    get available() {
      return available;
    },
    get selected(): HostInfo | undefined {
      return available.find((h) => h.id === selectedId);
    },
    set,
    setAvailable
  };
}

export const hosts = createHostStore();
