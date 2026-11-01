// Selected host for the multi-host views. The selection is persisted in
// localStorage so navigating between pages doesn't reset it, and the
// available list is refreshed on demand from /api/v1/hosts.
//
// Slice P.6: introduces the "all" pseudo-host — a virtual selection that
// tells every list page to request ?host=all and render aggregated rows
// from every online host. The sentinel matches internal/host.AllHostsID
// on the backend. `all` is only selectable when at least two real hosts
// exist (local + one or more agents) so single-host installs never see
// a confusing extra entry in the picker.

import type { HostInfo } from '$lib/api';

const STORAGE_KEY = 'dockmesh_selected_host';

// Virtual "all hosts" entry — not returned by /api/v1/hosts, injected by
// the store when appropriate. Kind='all' differentiates it from real
// hosts so the layout can render a distinct pill / icon.
const ALL_HOSTS: HostInfo = {
  id: 'all',
  name: 'All hosts',
  kind: 'all',
  status: 'online'
};

export const ALL_HOSTS_ID = 'all';

export function isAllHosts(id: string): boolean {
  return id === ALL_HOSTS_ID;
}

function loadInitial(): string {
  if (typeof window === 'undefined') return 'local';
  return window.localStorage.getItem(STORAGE_KEY) || 'local';
}

export function createHostStore() {
  let selectedId = $state<string>(loadInitial());
  // `available` is the list pulled from /api/v1/hosts. The ALL_HOSTS
  // entry is NOT stored here — it's injected at read time via the
  // `withAll` getter so the host picker can conditionally show it.
  let available = $state<HostInfo[]>([]);

  function set(id: string) {
    selectedId = id;
    if (typeof window !== 'undefined') {
      window.localStorage.setItem(STORAGE_KEY, id);
    }
  }

  function setAvailable(list: HostInfo[]) {
    available = list;
    // A caller limited to some hosts may not see local at all, so the
    // fallback is the first host the list actually offers.
    if (selectedId === ALL_HOSTS_ID) return;
    const usable = (h: HostInfo) => h.id === 'local' || h.status === 'online';
    if (list.some((h) => h.id === selectedId && usable(h))) return;
    if (list.length === 0) return;
    const local = list.find((h) => h.id === 'local');
    set((local ?? list.find(usable) ?? list[0]).id);
  }

  return {
    get id() {
      return selectedId;
    },
    get available() {
      return available;
    },
    // Host-picker-facing list: includes the ALL_HOSTS virtual entry at
    // the top when more than one real host exists. Used by the sidebar
    // dropdown so the "All hosts" option only shows in multi-host setups.
    get withAll(): HostInfo[] {
      if (available.length <= 1) return available;
      return [ALL_HOSTS, ...available];
    },
    get selected(): HostInfo | undefined {
      if (selectedId === ALL_HOSTS_ID) return ALL_HOSTS;
      return available.find((h) => h.id === selectedId);
    },
    get isAll(): boolean {
      return selectedId === ALL_HOSTS_ID;
    },
    // Whether the selection is a host the caller can read. Until the list
    // arrives it may still be a scoped user's stale 'local'; callers
    // without hosts.view never get a list and use the selection as it is.
    usable(canListHosts: boolean): boolean {
      return selectedId === ALL_HOSTS_ID || !canListHosts || available.some((h) => h.id === selectedId);
    },
    set,
    setAvailable
  };
}

export const hosts = createHostStore();
