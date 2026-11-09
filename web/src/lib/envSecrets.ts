export function isSecretKey(key: string): boolean {
  return /password|secret|token|api[_-]?key|credential|access[_-]?key/i.test(key);
}

// Hides the value of a KEY=value line whose key looks like a secret.
export function maskEnvLine(line: string): string {
  const eq = line.indexOf('=');
  if (eq <= 0 || !isSecretKey(line.slice(0, eq))) return line;
  return `${line.slice(0, eq + 1)}••••••••`;
}
