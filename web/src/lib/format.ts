// Shared text formats for lists and detail pages.
import { m } from '$lib/paraglide/messages.js';
import { fmtDate } from '$lib/i18n';

// "just now", "3 min ago", "2 h ago", "5 d ago", then the date.
export function fmtAgo(ts?: string | number | null, empty = '—'): string {
  if (ts == null || ts === '') return empty;
  const t = typeof ts === 'number' ? ts : Date.parse(ts);
  if (!t || isNaN(t)) return empty;
  const s = Math.max(0, (Date.now() - t) / 1000);
  if (s < 60) return m.time_just_now();
  if (s < 3600) return m.time_min_ago({ n: Math.floor(s / 60) });
  if (s < 86400) return m.time_h_ago({ n: Math.floor(s / 3600) });
  if (s < 30 * 86400) return m.time_d_ago({ n: Math.floor(s / 86400) });
  return fmtDate(t);
}

// SI units for image, file, disk, transfer and backup sizes, like Docker.
export function fmtBytes(n?: number | null, empty = '0 B'): string {
  if (!n || n < 1) return empty;
  const units = ['B', 'kB', 'MB', 'GB', 'TB', 'PB'];
  let v = n;
  let i = 0;
  while (v >= 1000 && i < units.length - 1) {
    v /= 1000;
    i++;
  }
  return `${v.toFixed(i === 0 ? 0 : 1)} ${units[i]}`;
}

// Binary units for memory and memory limits, like `docker stats`.
export function fmtMem(n?: number | null, empty = '0 B'): string {
  if (!n || n < 1) return empty;
  const units = ['B', 'KiB', 'MiB', 'GiB', 'TiB', 'PiB'];
  let v = n;
  let i = 0;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  return `${v.toFixed(i === 0 ? 0 : 1)} ${units[i]}`;
}

// Threshold class for a usage percentage: '' below warnAt, then warn, bad from 90.
export function pctTone(p: number, warnAt = 70): '' | 'warn' | 'bad' {
  return p >= 90 ? 'bad' : p >= warnAt ? 'warn' : '';
}
