import { untrack } from 'svelte';

// Number of open dialogs; toasts move to the top while one is open so
// they never cover a dialog's footer buttons.
let open = $state(0);

export const dialogs = {
  get open() {
    return open;
  },
  // Called from a dialog's effect; untracked so the effect does not
  // depend on the counter it changes.
  track(): () => void {
    untrack(() => open++);
    return () => {
      untrack(() => open--);
    };
  }
};
