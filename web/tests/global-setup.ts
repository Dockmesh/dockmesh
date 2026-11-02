import { request, type FullConfig } from '@playwright/test';
import { ADMIN, ROLE_USERS, password } from './env';

// The suite creates users and changes settings, so it only runs against an
// instance on this machine unless the caller says the target is disposable.
function assertThrowaway(baseURL: string) {
  const host = new URL(baseURL).hostname;
  if (['127.0.0.1', 'localhost', '::1', '[::1]'].includes(host)) return;
  if (process.env.DOCKMESH_E2E_THROWAWAY === '1') return;
  throw new Error(`refusing to seed users on ${baseURL}; set DOCKMESH_E2E_THROWAWAY=1 if that instance is disposable`);
}

export default async function globalSetup(config: FullConfig) {
  const baseURL = config.projects[0].use.baseURL as string;
  assertThrowaway(baseURL);
  const api = await request.newContext({ baseURL });
  const login = await api.post('/api/v1/auth/login', { data: { username: ADMIN, password: password() } });
  if (!login.ok()) throw new Error(`admin sign-in failed: ${login.status()} ${await login.text()}`);
  const { access_token: token } = await login.json();
  const headers = { Authorization: `Bearer ${token}` };

  // The browser locale of each project decides the language, not a saved choice.
  const lang = await api.put('/api/v1/me/language', { headers, data: { language: '' } });
  if (!lang.ok()) throw new Error(`reset language: ${lang.status()}`);

  const existing: { username: string }[] = await (await api.get('/api/v1/users', { headers })).json();
  for (const [role, username] of Object.entries(ROLE_USERS)) {
    if (existing.some((u) => u.username === username)) continue;
    const res = await api.post('/api/v1/users', { headers, data: { username, password: password(), role } });
    if (!res.ok()) throw new Error(`create ${username}: ${res.status()} ${await res.text()}`);
  }
  await api.dispose();
}
