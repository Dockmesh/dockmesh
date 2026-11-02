import { api, ApiError } from '$lib/api';
import { toast } from '$lib/stores/toast.svelte';
import { m } from '$lib/paraglide/messages.js';

// Key/value instance settings shared by the settings sub-pages. Saving
// writes every loaded key back, like the API expects.
export function createSettingsForm() {
  let values = $state<Map<string, string>>(new Map());
  let saved = $state<Map<string, string>>(new Map());
  let busy = $state(false);

  async function load() {
    try {
      const list = await api.system.settings();
      values = new Map(list.map((e) => [e.key, e.value]));
      saved = new Map(values);
    } catch (err) {
      toast.error(m.settings_load_failed(), err instanceof ApiError ? err.message : undefined);
    }
  }

  async function save() {
    busy = true;
    try {
      await api.system.updateSettings([...values.entries()].map(([key, value]) => ({ key, value })));
      saved = new Map(values);
      toast.success(m.settings_saved_toast());
    } catch (err) {
      toast.error(m.settings_save_failed(), err instanceof ApiError ? err.message : undefined);
    } finally {
      busy = false;
    }
  }

  return {
    load,
    save,
    get: (key: string) => values.get(key) ?? '',
    set(key: string, value: string) {
      const next = new Map(values);
      next.set(key, value);
      values = next;
    },
    discard() {
      values = new Map(saved);
    },
    get busy() {
      return busy;
    },
    get dirty() {
      if (values.size !== saved.size) return true;
      for (const [k, v] of values) if (saved.get(k) !== v) return true;
      return false;
    }
  };
}
