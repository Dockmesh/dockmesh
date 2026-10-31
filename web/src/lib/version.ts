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
