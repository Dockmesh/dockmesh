/**
 * Formats a Dockmesh version for display. The server reports release
 * versions with a leading "v" and agents may or may not, so release
 * versions are normalised to one "v"; dev and branch builds stay as they
 * are.
 */
export function fmtVersion(v: string | null | undefined): string {
  if (!v) return '';
  const bare = v.replace(/^v/, '');
  return /^\d/.test(bare) ? `v${bare}` : v;
}

// Which part of two x.y.z versions differs first; null when either is not
// such a version or both are the same.
export function semverBump(a: string, b: string): 'major' | 'minor' | 'patch' | null {
  const re = /^(\d+)\.(\d+)\.(\d+)/;
  const ma = re.exec(a);
  const mb = re.exec(b);
  if (!ma || !mb) return null;
  if (ma[1] !== mb[1]) return 'major';
  if (ma[2] !== mb[2]) return 'minor';
  if (ma[3] !== mb[3]) return 'patch';
  return null;
}
