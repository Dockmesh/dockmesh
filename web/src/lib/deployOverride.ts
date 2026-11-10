import { api } from '$lib/api';
import { confirm } from '$lib/stores/confirm.svelte';
import { m } from '$lib/paraglide/messages.js';

// confirmOverride asks before a deploy that merges the stack's default
// environment override, since the Deploy button alone doesn't show it.
// True when the stack has none, it can't be read, or the user agreed.
export async function confirmOverride(name: string): Promise<boolean> {
  let active = '';
  try {
    active = (await api.stacks.getEnvironments(name)).active;
  } catch {
    return true;
  }
  if (!active) return true;
  return confirm.ask({
    title: m.stacks_override_title({ env: active }),
    message: m.stacks_override_message({ name, env: active, file: `compose.${active}.yaml` }),
    body: m.stacks_override_body({ file: `compose.${active}.yaml` }),
    confirmLabel: m.stacks_override_confirm({ env: active })
  });
}
