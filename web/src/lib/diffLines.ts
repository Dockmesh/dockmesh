// LCS-based unified diff between two multi-line strings. Compose
// files are short enough (typically <300 lines) that the O(m·n)
// table is cheap. Returns context lines plus +/- markers.
export type DiffLine = { kind: ' ' | '-' | '+'; text: string };
export function diffLines(oldText: string, newText: string): DiffLine[] {
  const o = oldText.split('\n');
  const n = newText.split('\n');
  const M = o.length, N = n.length;
  const lcs: number[][] = Array.from({ length: M + 1 }, () => new Array(N + 1).fill(0));
  for (let i = 0; i < M; i++) {
    for (let j = 0; j < N; j++) {
      if (o[i] === n[j]) lcs[i + 1][j + 1] = lcs[i][j] + 1;
      else lcs[i + 1][j + 1] = Math.max(lcs[i + 1][j], lcs[i][j + 1]);
    }
  }
  const out: DiffLine[] = [];
  let i = M, j = N;
  while (i > 0 || j > 0) {
    if (i > 0 && j > 0 && o[i - 1] === n[j - 1]) {
      out.push({ kind: ' ', text: o[i - 1] }); i--; j--;
    } else if (j > 0 && (i === 0 || lcs[i][j - 1] >= lcs[i - 1][j])) {
      out.push({ kind: '+', text: n[j - 1] }); j--;
    } else {
      out.push({ kind: '-', text: o[i - 1] }); i--;
    }
  }
  return out.reverse();
}
