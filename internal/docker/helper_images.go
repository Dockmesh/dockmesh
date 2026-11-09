package docker

// BusyboxHelperImage runs the helper containers that get volume or stack
// data mounted, so it is pinned to the multi-arch index digest: a tag alone
// could be replaced on the registry.
const BusyboxHelperImage = "busybox:1.38@sha256:a46257e06c82ad85242d0d38077d0a52d9896389ca60a1a59d3463db586e5fa4"
