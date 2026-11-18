export type StepId = 'W1' | 'W2' | 'N1' | 'N2' | 'N3' | 'N4' | 'N5' | 'N6' | 'N7' | 'R1' | 'R2' | 'R3';
export type SetupPath = 'new' | 'restore';
export type TLSChoice = 'internal' | 'file' | 'caddy' | 'proxy';

export const NEW_PATH: StepId[] = ['W1', 'W2', 'N1', 'N2', 'N3', 'N4', 'N5', 'N6', 'N7'];
export const RESTORE_PATH: StepId[] = ['W1', 'W2', 'R1', 'R2', 'R3'];

export function pathSteps(path: SetupPath | null): StepId[] {
  return path === 'restore' ? RESTORE_PATH : NEW_PATH;
}

/** Before the choice in W2 the total is not known yet. */
export function isEarly(step: StepId): boolean {
  return step === 'W1' || step === 'W2';
}

export const DOCS_BASE = 'https://dockmesh.dev/docs/';

export const DOCS: Record<StepId, string> = {
  W1: 'install/setup#setup-code',
  W2: 'install/setup',
  N1: 'admin/users',
  N2: 'install/setup#data-directory',
  N3: 'install/setup#tls',
  N4: 'features/reverse-proxy',
  N5: 'features/notifications',
  N6: 'install/setup',
  N7: 'install/setup#what-setup-writes',
  R1: 'install/setup#data-directory',
  R2: 'features/backups#restore-on-a-new-server',
  R3: 'features/backups#restore-on-a-new-server'
};

/**
 * A done step links back to itself, except the code step; nothing links
 * while Apply runs, and later steps never do.
 */
export function railClickable(target: StepId, current: StepId, path: SetupPath | null): boolean {
  if (target === 'W1' || current === 'N7' || current === 'R3') return false;
  const steps = pathSteps(path);
  return steps.indexOf(target) >= 0 && steps.indexOf(target) < steps.indexOf(current);
}

/** Upper case without spaces and dashes: what the server compares. */
export function normalizeCode(raw: string): string {
  return raw.toUpperCase().replace(/[\s-]+/g, '');
}

export function codeFormatOK(raw: string): boolean {
  return /^[0-9A-Z]{12}$/.test(normalizeCode(raw));
}

/**
 * The code the installer link carries after `#`. Anything that cannot be
 * a code is ignored, so a crafted link can only put a short string of
 * letters, digits, spaces and dashes into the field.
 */
export function codeFromFragment(hash: string): string {
  let v = hash.startsWith('#') ? hash.slice(1) : hash;
  if (!v || v.length > 64) return '';
  try {
    v = decodeURIComponent(v);
  } catch {
    return '';
  }
  v = v.trim();
  if (!/^[0-9A-Za-z -]{1,32}$/.test(v)) return '';
  return v.toUpperCase();
}

export type FieldError =
  | 'username'
  | 'email'
  | 'password_short'
  | 'password_is_username'
  | 'password_mismatch'
  | null;

const USERNAME_RE = /^[a-z0-9._-]{3,32}$/;

export function usernameError(v: string): FieldError {
  return USERNAME_RE.test(v) ? null : 'username';
}

export function emailError(v: string): FieldError {
  if (v === '') return null;
  return /^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(v) && v.length <= 254 ? null : 'email';
}

export function passwordError(pw: string, username: string, min: number): FieldError {
  if ([...pw].length < min) return 'password_short';
  if (pw === username) return 'password_is_username';
  return null;
}

export function repeatError(pw: string, repeat: string): FieldError {
  return pw === repeat ? null : 'password_mismatch';
}

const PW_CHARS = 'ABCDEFGHJKMNPQRSTUVWXYZabcdefghijkmnpqrstuvwxyz23456789-_.!';

/** Characters from an unambiguous set, without modulo bias. */
export function generatePassword(len = 20, rand: (a: Uint8Array<ArrayBuffer>) => Uint8Array = (a) => crypto.getRandomValues(a)): string {
  const limit = 256 - (256 % PW_CHARS.length);
  let out = '';
  while (out.length < len) {
    for (const b of rand(new Uint8Array(len * 2))) {
      if (b < limit && out.length < len) out += PW_CHARS[b % PW_CHARS.length];
    }
  }
  return out;
}

export type Strength = 0 | 1 | 2 | 3 | 4;

export function passwordStrength(pw: string): Strength {
  if (!pw) return 0;
  let s = 0;
  if (pw.length >= 12) s++;
  if (pw.length >= 16) s++;
  if (/[A-Z]/.test(pw) && /[a-z]/.test(pw) && /\d/.test(pw)) s++;
  if (/[^A-Za-z0-9]/.test(pw) && pw.length >= 12) s++;
  return Math.min(4, s) as Strength;
}

export type URLError = 'https_required' | 'proxy_url' | 'invalid' | null;

export function baseURLError(v: string, tls: TLSChoice): URLError {
  let u: URL;
  try {
    u = new URL(v);
  } catch {
    return 'invalid';
  }
  if (!u.hostname || /\s/.test(v)) return 'invalid';
  if (tls === 'proxy') return u.protocol === 'https:' ? null : 'proxy_url';
  return u.protocol === 'https:' ? null : 'https_required';
}

export function portError(v: string): boolean {
  if (!/^\d{1,5}$/.test(v)) return true;
  const n = Number(v);
  return n < 1 || n > 65535;
}

/** The agent URL the server derives from the base URL and agent port. */
export function agentURL(base: string, agentPort: string): string {
  try {
    const host = new URL(base).hostname;
    const h = host.includes(':') && !host.startsWith('[') ? `[${host}]` : host;
    return `wss://${h}:${agentPort}/connect`;
  } catch {
    return '';
  }
}

export function listenAddressError(v: string): boolean {
  const ipv4 = /^(25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)(\.(25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)){3}$/;
  return !(ipv4.test(v) || (v.includes(':') && /^[0-9a-fA-F:.]+$/.test(v)));
}

export function isLoopback(addr: string): boolean {
  return addr.startsWith('127.') || addr === '::1';
}

/** The address the server listens on, as DOCKMESH_HTTP_ADDR wants it. */
export function httpAddr(tls: TLSChoice, listen: string, port: string): string {
  // Empty lets the server pick its loopback default for Caddy.
  if (tls === 'caddy') return '';
  if (tls !== 'proxy') return `:${port}`;
  return listen.includes(':') ? `[${listen}]:${port}` : `${listen}:${port}`;
}

/** Fingerprint in two lines of 16 bytes, as the installer prints it. */
export function fingerprintLines(fp: string): string[] {
  const parts = fp.split(':');
  if (parts.length !== 32) return [fp];
  return [parts.slice(0, 16).join(':') + ':', parts.slice(16).join(':')];
}

/** Whether a wss:// agent URL is one the server accepts. */
export function agentURLError(v: string): boolean {
  try {
    const u = new URL(v);
    return u.protocol !== 'wss:' || !u.hostname || /\s/.test(v);
  } catch {
    return true;
  }
}

export type TrustedError = { kind: 'invalid' } | { kind: 'all'; net: string } | null;

/** Checks a list of addresses or networks like the server's parser. */
export function trustedProxiesError(v: string): TrustedError {
  const items = v.split(/[\s,;]+/).filter(Boolean);
  if (items.length === 0 || items.length > 32) return { kind: 'invalid' };
  for (const it of items) {
    const [addr, bits, extra] = it.split('/');
    if (extra !== undefined || listenAddressError(addr)) return { kind: 'invalid' };
    if (bits === undefined) continue;
    if (!/^\d{1,3}$/.test(bits) || Number(bits) > (addr.includes(':') ? 128 : 32)) return { kind: 'invalid' };
    if (Number(bits) === 0) return { kind: 'all', net: it };
  }
  return null;
}

/** The trusted proxy the server uses for a listen address: the address itself, none for a wildcard. */
export function defaultTrustedProxies(listen: string): string {
  if (/^[0.:]+$/.test(listen)) return '';
  return listen.includes(':') ? `${listen}/128` : `${listen}/32`;
}

/** Whether stacks lies outside dataDir, so root has to create it. */
export function stacksOutside(dataDir: string, stacks: string): boolean {
  const d = dataDir.replace(/\/+$/, '');
  return stacks !== d && !stacks.startsWith(d + '/');
}
