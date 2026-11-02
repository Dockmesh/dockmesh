import {
  baseLocale,
  extractLocaleFromNavigator,
  getLocale,
  locales,
  setLocale,
  type Locale
} from '$lib/paraglide/runtime.js';

export const LANGUAGES: { id: Locale; label: string }[] = [
  { id: 'en', label: 'English' },
  { id: 'de', label: 'Deutsch' }
];

export function isLocale(v: unknown): v is Locale {
  return typeof v === 'string' && (locales as readonly string[]).includes(v);
}

// A saved choice wins; without one the browser language decides. Paraglide
// keeps the resolved locale in a cookie, so a choice cleared on the account
// is undone by switching to the browser's language, which reloads once.
export function applyLanguagePref(pref: string | undefined | null): void {
  if (typeof document === 'undefined') return;
  const want = isLocale(pref) ? pref : (extractLocaleFromNavigator() ?? baseLocale);
  if (want !== getLocale()) setLocale(want);
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
