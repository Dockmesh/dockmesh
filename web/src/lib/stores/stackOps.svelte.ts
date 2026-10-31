// Tracks in-flight stack operations (deploy / stop / save / delete) so the
// "busy" state survives navigating away and back to the stack detail page
// while a long-running request is still pending; page-local state resets
// on every mount.
//
// Keyed by `${hostId}:${stackName}` because the same stack name can exist
// on different hosts and we only want to lock the one being mutated.

import { SvelteSet } from 'svelte/reactivity';

const inflight = new SvelteSet<string>();

export function stackOpKey(hostId: string, name: string): string {
  return `${hostId}:${name}`;
}

export const stackOps = {
  isBusy(hostId: string, name: string): boolean {
    return inflight.has(stackOpKey(hostId, name));
  },

  async run<T>(hostId: string, name: string, fn: () => Promise<T>): Promise<T> {
    const key = stackOpKey(hostId, name);
    if (inflight.has(key)) {
      throw new Error('Another operation is already running for this stack');
    }
    inflight.add(key);
    try {
      return await fn();
    } finally {
      inflight.delete(key);
    }
  }
};
