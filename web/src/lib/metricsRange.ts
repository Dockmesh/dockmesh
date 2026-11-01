export type MetricsRange = 'live' | '1h' | '6h' | '24h' | '7d' | '30d';

// Window of each history range in seconds; callers pick their own live window.
export const HISTORY_RANGE_S: Record<Exclude<MetricsRange, 'live'>, number> = {
  '1h': 3600,
  '6h': 21600,
  '24h': 86400,
  '7d': 604800,
  '30d': 2592000
};

// Stored resolution that keeps a chart of the given span at a few hundred points.
export function historyResolution(seconds: number): 'raw' | '1m' | '1h' {
  if (seconds <= 6 * 3600) return 'raw';
  if (seconds <= 7 * 86400) return '1m';
  return '1h';
}
