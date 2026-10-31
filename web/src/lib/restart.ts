// waitForRestart polls the health endpoint until the server has gone
// away and come back, or until the timeout.
export async function waitForRestart(timeoutMs = 180_000): Promise<boolean> {
  const deadline = Date.now() + timeoutMs;
  let wentDown = false;
  while (Date.now() < deadline) {
    await new Promise((r) => setTimeout(r, 2000));
    try {
      const res = await fetch('/api/v1/health', { cache: 'no-store' });
      if (res.ok && wentDown) return true;
      if (!res.ok) wentDown = true;
    } catch {
      wentDown = true;
    }
  }
  return false;
}
