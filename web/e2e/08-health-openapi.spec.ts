import { test as base, expect } from '@playwright/test';

// Infra-level endpoints. These don't need auth — they're either probes
// (liveness/readiness) or public API documentation.

base.describe('health + openapi', () => {
	base('liveness probe returns 200', async ({ request }) => {
		const resp = await request.get('/healthz/live');
		expect(resp.status()).toBe(200);
	});

	base('readiness probe returns 200', async ({ request }) => {
		const resp = await request.get('/healthz/ready');
		expect(resp.status()).toBe(200);
	});

	base('OpenAPI JSON is served', async ({ request }) => {
		const resp = await request.get('/api/v1/openapi.json');
		expect(resp.status()).toBe(200);
		const body = await resp.json();
		expect(body.openapi).toMatch(/^3\./);
		expect(body.info?.title).toBeTruthy();
	});

	base('Swagger UI docs page renders', async ({ request }) => {
		const resp = await request.get('/api/v1/docs');
		expect(resp.status()).toBe(200);
		const text = await resp.text();
		// The Swagger UI HTML always contains this marker.
		expect(text.toLowerCase()).toMatch(/swagger|openapi/);
	});
});
