// Number of open dialogs; toasts move to the top while one is open so
// they never cover a dialog's footer buttons.
let open = $state(0);

export const dialogs = {
  get open() {
    return open;
  },
  track(): () => void {
    open++;
    return () => {
      open--;
    };
  }
};
