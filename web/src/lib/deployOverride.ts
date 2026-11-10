import { api } from '$lib/api';
import { confirm } from '$lib/stores/confirm.svelte';
import { m } from '$lib/paraglide/messages.js';

// confirmOverride asks before a deploy that merges the stack's default
// environment override, since the Deploy button alone doesn't show it.
// It returns the override the user agreed to ('' for none), which the
// deploy then names explicitly so a default changed in the meantime is
// not applied unseen, or null when the user cancelled.
export async function confirmOverride(name: string): Promise<string | null> {
  const active = (await api.stacks.getEnvironments(name)).active;
  if (!active) return '';
  const ok = await confirm.ask({
    title: m.stacks_override_title({ env: active }),
    message: m.stacks_override_message({ name, env: active, file: `compose.${active}.yaml` }),
    body: m.stacks_override_body({ file: `compose.${active}.yaml` }),
    confirmLabel: m.stacks_override_confirm({ env: active })
  });
  return ok ? active : null;
}
