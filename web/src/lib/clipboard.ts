// copyToClipboard puts `text` on the user's clipboard regardless of
// whether the page is loaded over HTTPS, localhost, or a plain-HTTP LAN
// address. Browsers gate `navigator.clipboard` behind secure contexts —
// hitting Dockmesh at `http://192.168.10.90:8080` from another machine
// would otherwise mean every Copy button silently fails.
//
// Returns true on success, false on every failure (including
// "execCommand is gone in modern browsers"). Callers decide how to
// surface that — toast.success / toast.error etc.
import { toast } from '$lib/stores/toast.svelte';

export async function copyToClipboard(text: string): Promise<boolean> {
  // Modern path — only works on https:// or localhost.
  if (
    typeof navigator !== 'undefined' &&
    navigator.clipboard?.writeText &&
    (typeof window === 'undefined' || window.isSecureContext)
  ) {
    try {
      await navigator.clipboard.writeText(text);
      return true;
    } catch {
      // Fall through to the legacy path. Browsers can still reject
      // clipboard writes in iframes or after losing focus.
    }
  }

  // Legacy path — document.execCommand('copy') against a transient
  // textarea. Works in plain-HTTP contexts because it predates the
  // Permissions API. Deprecated by W3C but every shipped browser still
  // supports it as of 2026 and there is no replacement for non-secure
  // contexts.
  if (typeof document === 'undefined') return false;
  try {
    const ta = document.createElement('textarea');
    ta.value = text;
    // Off-screen but selectable — display:none would break the copy.
    ta.style.position = 'fixed';
    ta.style.top = '0';
    ta.style.left = '0';
    ta.style.width = '1px';
    ta.style.height = '1px';
    ta.style.opacity = '0';
    ta.style.pointerEvents = 'none';
    ta.setAttribute('readonly', '');
    document.body.appendChild(ta);
    ta.focus();
    ta.select();
    ta.setSelectionRange(0, text.length);
    const ok = document.execCommand('copy');
    document.body.removeChild(ta);
    return ok;
  } catch {
    return false;
  }
}

// copyWithToast is the one-liner most callers want: copy + toast on
// outcome. successTitle is required; pass detail as the second message
// arg if you want a sub-line. Returns the boolean from copyToClipboard
// so callers can branch further (e.g. close a modal on success only).
export async function copyWithToast(
  text: string,
  successTitle: string,
  successDetail?: string,
): Promise<boolean> {
  const ok = await copyToClipboard(text);
  if (ok) {
    toast.success(successTitle, successDetail);
  } else {
    toast.error('Copy failed', 'Select the text manually and use Ctrl+C');
  }
  return ok;
}
