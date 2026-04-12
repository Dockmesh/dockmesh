// All data fetching is client-side (localStorage auth, API calls).
// Disable SSR — adapter-static would prerender these otherwise.
export const ssr = false;
export const prerender = false;
