import { getLocale, setLocale, locales, cookieName, type Locale } from '$lib/paraglide/runtime.js';

export type LanguagePref = '' | Locale;

export const LANGUAGES: { id: Locale; label: string }[] = [
  { id: 'en', label: 'English' },
  { id: 'de', label: 'Deutsch' }
];

export function isLocale(v: unknown): v is Locale {
  return typeof v === 'string' && (locales as readonly string[]).includes(v);
}

export function locale(): Locale {
  return getLocale();
}

function hasLocaleCookie(): boolean {
  return document.cookie.split(';').some((c) => c.trim().startsWith(`${cookieName}=`));
}

// The browser language only decides while the user has no saved choice, so an
// empty preference drops the cookie that would otherwise pin the last one.
export function applyLanguagePref(pref: string | undefined | null): void {
  if (typeof document === 'undefined') return;
  if (isLocale(pref)) {
    if (pref !== getLocale() || !hasLocaleCookie()) setLocale(pref);
    return;
  }
  if (!hasLocaleCookie()) return;
  document.cookie = `${cookieName}=; path=/; max-age=0`;
  window.location.reload();
}

export function fmtDate(t: number | string | Date): string {
  return new Date(t).toLocaleDateString(getLocale());
}

export function fmtDateTime(t: number | string | Date, opts?: Intl.DateTimeFormatOptions): string {
  return new Date(t).toLocaleString(getLocale(), opts);
}

export function fmtTime(t: number | string | Date, opts?: Intl.DateTimeFormatOptions): string {
  return new Date(t).toLocaleTimeString(getLocale(), opts);
}

export function fmtNumber(n: number, opts?: Intl.NumberFormatOptions): string {
  return n.toLocaleString(getLocale(), opts);
}
