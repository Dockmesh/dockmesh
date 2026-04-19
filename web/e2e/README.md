# v1 Regression Suite

Playwright E2E suite that covers the v1 acceptance matrix end-to-end.

## Run it

```bash
cd web
# against the default dev host
npx playwright test --project=v1-regression

# against a different server
DOCKMESH_URL=https://my-dockmesh.example.com \
DOCKMESH_USER=admin \
DOCKMESH_PASS=secret \
  npx playwright test --project=v1-regression

# Shortcut via Makefile (from repo root)
make test-e2e
```

## What it covers

| Spec | Area |
|---|---|
| `01-auth.spec.ts` | Login / logout / sessions panel |
| `02-dashboard.spec.ts` | Dashboard Local + All-hosts mode, metric cards, quick actions |
| `03-stacks.spec.ts` | Full stack lifecycle: create → deploy → history → rollback → delete |
| `04-containers.spec.ts` | List, detail, logs streaming |
| `05-resources.spec.ts` | Read-only page renders for images / volumes / networks / agents / migrations / proxy / environment / templates |
| `06-backups-alerts.spec.ts` | Backup target + alert channel CRUD via API + UI verification |
| `07-settings.spec.ts` | All 8 settings tabs load without console / page errors |
| `08-health-openapi.spec.ts` | `/healthz/live`, `/healthz/ready`, `/api/v1/openapi.json`, `/api/v1/docs` |

## How it handles state

- Every test that creates resources uses a unique suffix (`e2e-*-{timestamp}`) so re-runs don't collide.
- `test.afterAll` hooks clean up created stacks / targets / channels even when tests in the middle fail.
- Retries are set to 2 globally — WebSocket log streams and image pulls have inherent variance; a test failing on attempt 1 but green on retry 2 is almost always flake, not a regression.
- Workers are fixed at 1 because tests share server state (one Docker daemon, one filesystem).

## Reading the report

After a run, `web/playwright-report/index.html` has screenshots, traces, and video for every failure. Open it with:

```bash
npx playwright show-report
```

## When a test fails

1. Check the HTML report for the screenshot + trace.
2. If it looks like flake (a timeout near an image pull, a WS race), re-run — retries should have caught it.
3. If it's a real regression, the trace shows every action + request.
