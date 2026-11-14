---
title: Reverse proxy
description: The optional Caddy container Dockmesh manages on the server host, how to turn it on, sites, site options, path routes, upstream TLS, Caddyfile import, Caddy versions and certificates.
---

Dockmesh can run a Caddy reverse proxy for you. It isn't built into the binary: when the proxy is enabled, Dockmesh runs Caddy as a Docker container on the server host and configures it through Caddy's admin API. Caddy serves your sites over HTTPS with certificates it obtains itself (see [TLS modes](#tls-modes)). The proxy is off by default. If you already run Traefik, nginx or another proxy, keep it off.

Everything below happens on **Reverse proxy** in the sidebar, which has the tabs **Sites**, **Certificates**, **Activity** and **Caddy**.

## The proxy container

| Property | Value |
|---|---|
| Container name | `dockmesh-proxy` |
| Image | `caddy:2.11.7`, pinned by digest (see [Caddy version](#caddy-version)) |
| Network | host network, listens on `:80` and `:443` |
| Admin API | Unix socket `<data dir>/caddy-admin/admin.sock`, reachable only by the Dockmesh user. The managed proxy needs Dockmesh installed directly on the host, not in a container |
| Restart policy | `unless-stopped` |
| Volumes | `dockmesh-caddy-data` (certificates), `dockmesh-caddy-config` (config) |
| Labels | `dockmesh.managed=true`, `dockmesh.component=proxy` |

Ports 80 and 443 must be free on the server host. The admin API has no authentication; it only answers on loopback and refuses requests from browsers on other sites. Any process on the server host can still reconfigure Caddy, so don't run untrusted workloads with host networking on the same machine.

Caddy starts from the last config Dockmesh pushed (`caddy run --resume`), so a container that restarts on its own keeps serving its sites. Dockmesh also compares Caddy's running config with the saved sites every 30 seconds and pushes them again if they differ. On a server start the container is kept when it still matches the expected image and settings; otherwise it is replaced.

In the containers list `dockmesh-proxy` is marked as a system container and shown read-only. Only admins can stop, remove or update it, and only admins can browse or back up its two volumes, since they hold the TLS private keys and the proxy's credentials. See [Containers](./containers.md#dockmeshs-own-containers).

## Turn it on and off

Click **Enable proxy** on the **Reverse proxy** page. Dockmesh pulls the Caddy image if needed, starts the container and applies every saved site. The choice is stored in the `proxy_enabled` setting, so the proxy starts again on every server boot. If the container exists but isn't running, the page offers **Start** instead.

To turn it off, open the actions menu next to **New site** and choose **Stop the proxy…**. This stops and removes the container and stores `proxy_enabled=false`. Sites stay saved, and both volumes are kept, so issued certificates survive.

Enabling needs `proxy.create`, stopping needs `proxy.delete` (`POST /api/v1/proxy/enable`, `POST /api/v1/proxy/disable`). `DOCKMESH_PROXY_ENABLED=true` in the server environment turns the proxy on only as long as `proxy_enabled` has never been stored. Once you enable or stop the proxy on the page, the stored choice wins over the variable.

## Sites

Click **New site** and enter the hostname. The dialog then asks where the site should go:

| Choice | What you pick |
|---|---|
| **A container** | A container on the server and one of its published ports; the upstream becomes `localhost:<port>` |
| **Another address** | Any `host:port`, such as a NAS, a router or a service on another machine (`192.168.10.40:5001`) |

Caddy runs on the server's host network, so it reaches containers on the server through their published ports. Docker container names don't resolve there. Containers on agent hosts are reached like any other machine, by the host's address and a published port.

Then choose the certificate (see [TLS modes](#tls-modes)) and, optionally, open the **Advanced** step to set [site options](#site-options). **Create site** saves the site.

Rules Dockmesh checks:

- One hostname per site. It can't be `localhost`, a loopback address or a bare `*`.
- The upstream must be a plain `host:port`: no scheme, path, placeholder or socket, and it can't point at the proxy's admin API.

A site that breaks these rules (for example one saved by an older version) isn't served, and admins get a notice listing it.

Creating a site runs as an operation with live progress: **Save the site**, **Apply the Caddy configuration** and **Get a certificate**. The certificate step waits up to 3 minutes for Caddy to obtain or serve the certificate; after that, or when you cancel it, the site stays and Caddy keeps trying. The step is skipped for HTTP-only sites. The progress also appears in the operations indicator in the top bar, and **Show last creation steps** in a site's menu opens it again. See [Monitoring](./monitoring.md#running-operations).

Every change sends the whole config to Caddy's `/load` endpoint, without restarting the container. A site can be switched off and keeps its settings. Click a site to open its page, with the tabs **Overview**, **Options** and **Config**.

| Action | Permission |
|---|---|
| View sites, certificates, activity, version and settings | `proxy.view` |
| Enable the proxy, create sites, import a Caddyfile | `proxy.create` |
| Edit sites and options, switch them on or off, change the Caddy version and proxy settings | `proxy.update` |
| Delete sites, stop the proxy | `proxy.delete` |

The proxy is server-wide: these permissions only count from an assignment on everything. See [Permissions](../concepts/permissions.md).

## Site options

The **Options** tab of a site edits its options in six groups. Every option starts at Caddy's default. Options with side effects carry a warning that says what happens. **Save options** sends the whole set; Dockmesh checks every value and shows a rejected one next to its field. The **Config** tab shows the Caddy JSON the saved site produces. Users without `proxy.update` see the options read-only, with header values hidden.

| Group | Options |
|---|---|
| **Routing** | **More upstreams** (requests are shared with the site's destination), **Load balancing** (**Random (Caddy default)**, **Round robin**, **Fewest connections**, **First available**, **Same client, same upstream**), **Health check path**, **Redirect www.&lt;host&gt;**, [upstream TLS](#upstream-tls), **Requests that match no path** (proxy to the backend, redirect, or respond 404; see [Path routes](#path-routes)) |
| **HTTPS** | **Also serve plain HTTP** instead of redirecting, HSTS (one year) |
| **Performance** | **Stream responses** (for server-sent events and live logs), **Buffer uploads** (up to 4 MiB per request; off streams uploads through), **Connect timeout**, **Read timeout**, **Write timeout** (durations like `30s` or `1h`, up to `168h`), **Largest upload**, **Keep streams on reload** (WebSockets stay open this long after a config change), **Compress responses** (zstd or gzip) |
| **Access** | **Password protection** (basic auth users), **IP addresses** (**Everyone**, **Only the addresses below**, **Everyone except the addresses below**), **Ask an auth service first** (forward auth, for example Authelia or Authentik, with headers copied from its answer) |
| **Headers** | **Security headers** (nosniff, SAMEORIGIN frames, strict referrer policy), **Send X-Forwarded-Port**, headers set on or removed from requests to the upstream and responses to visitors |
| **Logs** | **Access log**: one line per request in `docker logs dockmesh-proxy` |

Values can't contain Caddy placeholders (`{…}`), and you can't add raw Caddy directives.

Some values are write-only: once saved they are never shown again, not even to admins or in the config preview, and leaving the field empty keeps the stored value.

- Basic-auth passwords (8 characters or more) are stored as bcrypt hashes.
- Request headers marked **Secret**, such as an `Authorization` or API-key header for the app. When the site gets an upstream it didn't have before, enter the value again. Response headers can't be secret, since every visitor receives them.
- The query of the forward-auth URL, which can hold a token. A URL saved without a query keeps the stored one as long as scheme and host stay the same; **Remove the stored query** drops it.

Users who can view but not edit the proxy see every header value as `********` and the forward-auth URL only as `scheme://host:port`.

Write-only keeps values out of the UI and the API. It doesn't protect them from someone who can edit the proxy and points another site, such as the auth service's own site, at a server they control.

Changing a site writes a `proxy.route_update` audit entry that lists what changed (`upstream`, `tls_mode`, `enabled`, `options.<group>`, `paths`), never the values.

### Upstream TLS

By default Caddy talks plain HTTP to the site's backend. **Use HTTPS to the backend** (Options › Routing) turns on TLS for backends that only speak HTTPS, such as a port 443 address:

| Setting | What it does |
|---|---|
| **Server name (SNI)** | The name sent in the TLS handshake and checked against the backend's certificate. A host name, no scheme or port |
| **Host header** | **Keep the visitor's host** (default) or **Use the backend's host**, for backends that serve several sites by name |
| **Certificate check** | **Trust public CAs** (default), **Trust this CA certificate** (paste one or more PEM certificates, up to 64 KB; a private key is refused and only the certificates are stored), or **Don't check the certificate** |

**Don't check the certificate** lets anyone between the proxy and the backend read and change the traffic; use your own CA certificate instead if you can. A site where the site or one of its paths uses it shows **TLS not verified** in the site list, and creating the site or any change to it that leaves the setting on writes a `proxy.route_tls_unverified` audit entry.

### X-Forwarded-Port

Caddy sends `X-Forwarded-For`, `X-Forwarded-Proto` and `X-Forwarded-Host` to the backend on its own, but not the port. **Send X-Forwarded-Port** (Options › Headers, off by default) adds it with the port of the listener that took the request: `443` for HTTPS and `80` for plain HTTP. It applies to the site's backend and to every proxy path. When it's off, an `X-Forwarded-Port` sent by the visitor reaches the backend unchanged; remove it under request headers if the backend trusts it. Behind a port mapping or a CDN the port can differ from the one the visitor used; set a request header named `X-Forwarded-Port` on the site or on a path to send your own value.

## Path routes

The **Paths** tab of a site sends parts of it to other backends, redirects them or blocks them. A typical case is an app behind one domain whose parts need different backends, caching or timeouts, for example Oracle APEX with `/ords` and `/i`.

Each path route has:

| Field | Values |
|---|---|
| **Match** | **Prefix** `/ords` matches `/ords` and everything below it (`/ords/x`), never `/ordsfoo`. **Exact** `/health` matches only that path. Paths are compared without case; a trailing `/` is dropped |
| **Action** | **Proxy**, **Redirect** (an absolute path or an http(s) URL, code 301, 302, 307 or 308) or **Respond** (404 or 403) |
| **Backend** (Proxy) | **Same as the site**, with the site's upstreams, load balancing and upstream TLS, or **Other backend**: its own `host:port` addresses, load balancing, health check and [upstream TLS](#upstream-tls) |
| **Strip prefix** (Proxy, prefix only) | Removes exactly the prefix before passing the request on: `/ords/x` reaches the backend as `/x`, `/ords` as `/` |
| **Request and response headers** (Proxy) | Applied after the site's headers. On the same name the path's value wins, also over the site's security headers and HSTS, and a header the path removes is removed even if the site sets it. The tab marks a response header that replaces one of the site's |
| **Timeouts and streaming** (Proxy) | Connect, read and write timeout, stream responses, buffer uploads, keep streams on reload. Empty fields keep the site's values |

- **Order:** paths are sorted automatically, exact paths first, then longer prefixes before shorter ones, so no path can hide another. The table shows that order.
- **Fallback:** the last row, **everything else**, handles requests that match no path: proxy to the site's backend (default), redirect, or respond 404. It is the same setting as Options › Routing › **Requests that match no path**.
- **Access rules come first:** the site's IP filter, password protection, forward auth, compression and upload limit apply to every path. A path can't bypass them.
- **Limits:** up to 50 paths per site. A path starts with `/`, is not `/` alone (that is the fallback), and can't contain `*`, `[`, `]`, `\`, `{` or `}`, spaces, `#`, `?`, control characters, `.` or `..` segments or a `%` without two hex digits after it. It can't start with `//` and has at most 256 characters.
- **Secrets:** request headers on a path can be **Secret** like the site's; they are matched by the path and header name, and need to be entered again when the path gets a backend address it didn't have. The site's own secret request headers are sent only to paths that use the site's backend; paths with another backend don't get them. Turning off HTTPS or the certificate check on a path's backend keeps its secrets, which then cross the network unprotected.
- **Respond is not access control:** a 403 on a path blocks that path at the proxy, but a backend may serve the same content under another form of the path, for example with a trailing `/` after an exact path. Protect content with the site's access rules.

A site without paths gets exactly the Caddy config it had before path routes existed. **Save paths** sends the whole list, and **Reset** drops unsaved changes. If Caddy refuses the result, nothing is saved and the tab says "Caddy rejected the configuration. Nothing was changed." Users without `proxy.update` see the paths read-only, with header values hidden.

Over the API the paths are the `paths` list of `POST /api/v1/proxy/routes` and `PUT /api/v1/proxy/routes/{id}`. A `PUT` without `paths` keeps the stored ones, `[]` removes all. Errors name the field as `paths[<i>].<field>`, `i` being the position in the sorted list.

Over the API the options are the `options` object of `POST /api/v1/proxy/routes` and `PUT /api/v1/proxy/routes/{id}`. An empty object keeps Caddy's defaults. A `PUT` without `options` keeps the stored ones, a `PUT` with `options` replaces them. `GET /api/v1/proxy/routes/{id}/config` returns the generated Caddy config of one site.

## Importing a Caddyfile

**Import Caddyfile** on the **Reverse proxy** page takes an existing Caddyfile. The proxy has to be running, because Caddy itself parses the file. Paste it and click **Analyze**: Dockmesh lists every site with how it maps to a site and its options, and changes nothing until you click **Import**.

Each site comes back as:

| Status | Meaning |
|---|---|
| **ready** | Everything was converted |
| **partial** | Imported, but the list under it names what wasn't, such as `file_server`, path rules, placeholders like `{remote_host}` or an own certificate (the site gets automatic HTTPS instead) |
| **skipped** | Not imported, with the reason, for example a block without `reverse_proxy`, a host that already has a site or an upstream Dockmesh doesn't accept |

What is converted: the hostnames and the `reverse_proxy` upstreams with their load balancing, health check and timeouts, `tls internal` and plain-HTTP sites, `encode`, `request_body` size limits, `basic_auth`, `forward_auth`, `header` and `request_header` set and remove rules, IP filters of the form `@x client_ip …` with `respond @x 403`, and the access log. A block listing several names becomes one site per name, and a `www.` block that only redirects becomes the site's www redirect. Global options aren't imported; the dialog lists them.

Imported sites start **disabled**, so you can check them before they serve traffic.

`import <name>` works for snippets defined earlier in the same Caddyfile; Dockmesh expands them before Caddy sees the file. Refused, so paste that content instead:

- imports of files or globs, and imports with arguments or a block,
- heredocs (`<<`),
- client certificate files (`client_auth`, `trusted_ca_cert_file`, `trusted_leaf_cert_file`, `trust_pool`),
- environment variables (`{$NAME}`).

Because of that check, the word `import` can't appear anywhere else in the file, not even in a comment. The file can be up to 256 KiB.

The API is `POST /api/v1/proxy/import` with `{"caddyfile": "…", "dry_run": true}`; without `dry_run` the sites are created. Importing needs `proxy.create` and is recorded as `proxy.import` in the audit log.

## Caddy version

The **Caddy** tab shows which Caddy version runs and the versions tested with your Dockmesh release, each pinned by digest. **Switch** replaces the container with the chosen version; sites are briefly unavailable. If Caddy doesn't come up with the current config, Dockmesh starts the previous version again and reports the switch as failed. **Go back** returns to the version before the last switch, whose image is kept until the next switch. The proxy has to be running to switch.

Without a chosen version, the proxy follows the default of each Dockmesh release. When a newer default is available, the page header shows "*version* recommended", and the version also appears on the **Updates** page (see [Image updates](./image-updates.md)).

With `DOCKMESH_PROXY_IMAGE` set in the server environment, Dockmesh runs that image and the version can't be changed here.

The API is `GET` and `PUT /api/v1/proxy/version` (`{"version": "2.11.6"}`) and `POST /api/v1/proxy/version/rollback`. A failed switch returns `502`.

## Proxy settings

The **Caddy** tab also holds the proxy-wide settings (`PUT /api/v1/proxy/settings`):

| Setting | API field | Values |
|---|---|---|
| **Certificate contact** | `acme_email` | Email the certificate authority uses for expiry warnings. Optional, required for ZeroSSL |
| **Certificate authority** | `acme_ca` | Let's Encrypt (empty), `letsencrypt_staging` for tests (browsers don't trust these certificates), or `zerossl` |
| **Trusted proxies** | `trusted_proxies` | `cloudflare` trusts Cloudflare's published edge ranges, so IP filters and logs see the visitor's address. Only enable it when all traffic comes through Cloudflare |

Switching versions and changing settings need `proxy.update` and are recorded in the audit log. Users without it see the certificate contact with its local part hidden.

## TLS modes

| Mode | Label in the UI | Behaviour |
|---|---|---|
| `auto` | **Let's Encrypt** | Caddy obtains a certificate over ACME from the configured certificate authority. The hostname must resolve to this server and port 80 must be reachable from the internet. |
| `internal` | **Internal CA** | Caddy issues a certificate from its own local CA. Browsers warn until they trust Caddy's root. |
| `none` | **HTTP only** | Served over plain HTTP on port 80, without a certificate or an HTTPS redirect. |

DNS-01 challenges and wildcard certificates can't be configured, and you can't upload your own certificate.

## Certificates and activity

The **Certificates** tab lists the HTTPS sites with issuer, days until expiry and a status: **valid**, **renew soon** (less than 14 days left, which means a renewal hasn't worked yet), **not served** (the name may not point to this server yet, or Caddy is still getting the certificate) or **site off**. Dockmesh reads the certificate by opening a TLS connection to each site on port 443, a few at a time in the background, and reuses the result for 15 minutes. The server looks the name up in its own DNS, so a site whose name only resolves on your clients (a hosts-file entry, a local DNS the server doesn't use) stays **not served** although Caddy serves it. Caddy renews certificates about 30 days before they expire.

Expiring certificates also show up elsewhere:

- **Needs attention** on the overview and under **Activity** lists certificates that expire within 30 days (critical within 7 days). See [Monitoring](./monitoring.md#needs-attention).
- A built-in alert rule notifies when a certificate expires within 14 days. See [Alerts](./alerts.md).

The **Activity** tab merges certificate events (obtained, renewed, failed) from Caddy's log with the sites created in the last 7 days. Certificate events are kept in memory, the last 200 since Caddy started; for the full log run `docker logs dockmesh-proxy`.

The site page shows requests, p95 latency and 5xx errors when Caddy reports them. The config Dockmesh pushes doesn't turn on Caddy's HTTP metrics, so these figures usually stay empty.

## Using your own proxy instead

Leave the proxy disabled, publish your services' ports in `compose.yaml` bound to loopback (`127.0.0.1:<port>:<port>`) when your proxy runs on the same host, and point your proxy at them. Dockmesh doesn't need to know about it.

See also [Ports](../reference/ports.md) and [Security hardening](../operations/hardening.md).
