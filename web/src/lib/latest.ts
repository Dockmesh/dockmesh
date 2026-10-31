// Request sequencing for page loads: each call to the returned function
// starts a new request and hands back a check that is true only while no
// newer request has started. Pages use it to drop responses that arrive
// after a host switch or a faster follow-up load.
//
//     const nextLoad = latest();
//     async function load() {
//       const current = nextLoad();
//       const res = await api.x.list(hosts.id);
//       if (!current()) return;
//       items = res;
//     }
export function latest(): () => () => boolean {
	let seq = 0;
	return () => {
		const mine = ++seq;
		return () => mine === seq;
	};
}
