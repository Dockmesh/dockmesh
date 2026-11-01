---
title: Reverse proxy
description: The optional Caddy container Dockmesh manages on the local host, how to turn it on, routes, TLS modes and what to watch out for.
---

Dockmesh can run a Caddy reverse proxy for you. It isn't built into the binary. When the proxy is enabled, Dockmesh starts a Docker container on the local host and configures it through Caddy's admin API. The proxy is off by default. If you already run Traefik, nginx or another proxy, keep it off.

## The proxy container

| Property | Value |
|---|---|
| Container name | `dockmesh-proxy` |
| Image | `caddy:2` (pulled if missing) |
| Network | host network, listens on `:80` and `:443` |
| Admin API | `127.0.0.1:2019` on the host, no authentication |
| Volumes | `dockmesh-caddy-data` (certificates), `dockmesh-caddy-config` |
| Labels | `dockmesh.managed=true`, `dockmesh.component=proxy` |

Ports 80 and 443 must be free on the local host. Because the admin API listens on loopback without authentication, any process on the server host can reconfigure Caddy. Don't run untrusted workloads with host networking on the same machine.

## Turn it on

There are two switches, and they behave differently:

- **Enable proxy** (or **Start**) on the **Proxy** page (`POST /api/v1/proxy/enable`) starts the container right away, but doesn't save the setting. After a server restart the proxy is off again in Dockmesh, and routes are no longer pushed to Caddy.
- The `proxy_enabled` setting is read at startup. If it's `true`, the server starts the proxy on every boot.

To keep the proxy on across restarts, set the setting. Either put `DOCKMESH_PROXY_ENABLED=true` in the server environment (Dockmesh copies it into the setting on the next start), or set it through the API, which also starts or stops the container:

```bash
curl -X PUT https://dockmesh.example.com/api/v1/settings \
  -H "Authorization: Bearer dmt_…" -H "Content-Type: application/json" \
  -d '[{"key":"proxy_enabled","value":"true"}]'
```

Changing settings needs `system.update`. Enabling the proxy from the **Proxy** page needs `proxy.create`, disabling it needs `proxy.delete`.

To turn it off for good, set `proxy_enabled` to `false` through the API and remove `DOCKMESH_PROXY_ENABLED` from the environment. As long as the variable is set to `true`, it overrides a stored `false` at startup. **Stop** on the **Proxy** page alone only lasts until the next restart. Disabling removes the container but keeps both volumes, so issued certificates survive.

<!-- update after: proxy: keep the Caddy container, resume routes, serve tls none as http -->
On every start with the proxy enabled, Dockmesh force-removes the `dockmesh-proxy` container and creates it again from `caddy:2`, then pushes the routes. The image tag isn't pinned, so a newer Caddy 2.x image is used whenever one is pulled. Routes exist only in Caddy's running config. If the container restarts on its own (Docker restart, host reboot without a Dockmesh restart), it comes up with no routes until Dockmesh pushes the config again, which happens on the next route change or server start.

## Routes

Click **Add route** on the **Proxy** page. A route has three fields:

| Field | Example | Notes |
|---|---|---|
| **Hostname** | `app.example.com` | The host name Caddy matches |
| **Upstream** | `127.0.0.1:3000` | `host:port` Caddy connects to |
| **TLS mode** | `auto` | See below |

Caddy runs with host networking, so it reaches containers through ports published on the host, like `127.0.0.1:3000`. Docker container names don't resolve from the host network. The upstream suggestions in the dialog offer `container-name:port` entries, and those won't connect unless the name resolves on the host. Dockmesh only checks that the upstream isn't empty.

Every change sends the whole config to Caddy's `/load` endpoint, with no container restart. A route can be switched off with its toggle and keeps its settings. Routes need `proxy.create`, `proxy.update` and `proxy.delete`. Reading them needs `proxy.view`.

## Site options

Open a site on the **Proxy** page and switch to its **Options** tab to change them. Each group starts at Caddy's defaults; options with side effects (plain HTTP next to HTTPS, HSTS, an IP allow list, forward auth, health checks, buffered uploads) carry a warning with the consequence. **Save options** sends the whole set; Dockmesh checks every value and shows a rejected one next to its field. When you create a site, the optional **Advanced** step offers the same groups, closed until you open one. The **Config** tab shows the Caddy JSON the saved site produces. Users who can only view the proxy see the options read-only.

Over the API the options are the `options` object of `POST /api/v1/proxy/routes` and `PUT /api/v1/proxy/routes/{id}`. An empty object keeps Caddy's defaults, so existing routes behave as before.

| Section | Options |
|---|---|
| `performance` | `flush_immediately` (stream responses, for SSE and live logs), `buffer_requests` (off streams uploads to the app), `dial_timeout`, `read_timeout`, `write_timeout` (`30s`, `1h`, up to `168h`), `max_body_bytes`, `stream_close_delay` (keeps WebSockets open after a config change), `compression` |
| `routing` | `extra_upstreams`, `lb_policy` (`round_robin`, `least_conn`, `first`, `client_ip_hash`), `health_path`, `www_redirect` |
| `https` | `allow_http` (serve HTTP too instead of redirecting), `hsts` |
| `access` | `basic_auth` users, `ip_mode` (`allow` or `deny`) with `ip_ranges`, `forward_auth` (`url`, `copy_headers`) |
| `headers` | `security` (nosniff, SAMEORIGIN framing, strict referrer), `request_set`, `request_remove`, `response_set`, `response_remove` |
| `logs` | `access_log` (one line per request in `docker logs dockmesh-proxy`) |

A `PUT` without `options` keeps the stored ones; a `PUT` with `options` replaces them. Values can't contain Caddy placeholders (`{…}`), and there's no way to add raw Caddy directives.

Some values are write-only: once saved they are never shown again, not even to admins, and leaving the field empty keeps the stored value.

- Basic-auth passwords are stored as bcrypt hashes; send a user without `password` to keep its password.
- Headers marked **Secret** (`"secret": true`, e.g. an `Authorization` or API-key header to the app) come back with an empty `value` and `value_set: true`; send them with an empty value to keep it.
- The query of the forward-auth URL (it can hold a token) is left out of every response, with `query_set: true`. A URL sent without a query keeps the stored one as long as scheme and host stay the same; for another server enter the query again, or set `clear_query` to drop it.

`GET /api/v1/proxy/routes/{id}/config` returns the Caddy config Dockmesh generates for one site, with password hashes, secret header values and the forward-auth query replaced. Users who can view proxy routes but not edit them (`proxy.update`) also see every other header value as `********`. Changing a site writes a `proxy.route_update` audit entry that lists what changed (`upstream`, `tls_mode`, `enabled`, `options.<group>`), never the values.

### Importing a Caddyfile

`POST /api/v1/proxy/import` with `{"caddyfile": "…", "dry_run": true}` shows how each site of an existing Caddyfile maps to a route and its options; without `dry_run` the sites are created **disabled**, so you can check them before they serve traffic. The proxy has to be running, because Caddy itself parses the file. Each site comes back as `ready`, `partial` (the `unsupported` list names what wasn't imported, such as `file_server`, path rules or placeholders like `{remote_host}`) or `skipped` (with the reason, for example a host that already has a route). `import <name>` works for snippets defined earlier in the same Caddyfile; imports of files or globs, imports with arguments, heredocs (`<<`), client certificate files and `{$ENV}` variables are refused, so paste that content instead. Because of that check, the word `import` can't appear anywhere else in the file either, not even in a comment. A block listing several names becomes one site per name, and a `www.` block that only redirects becomes the site's www redirect. Creating sites needs `proxy.create`.

## Caddy version and proxy settings

`GET /api/v1/proxy/version` lists the Caddy versions tested with your Dockmesh release (pinned by digest) and the one that runs. `PUT /api/v1/proxy/version` with `{"version": "2.11.6"}` recreates the proxy container with that version; if Caddy doesn't come up with the current config, the previous version is started again and the call fails with 502. `POST /api/v1/proxy/version/rollback` goes back to the version before the last switch, whose image is kept until the next switch. Without a chosen version the proxy follows the default of each Dockmesh release. With `DOCKMESH_PROXY_IMAGE` set, the version can't be changed here.

`PUT /api/v1/proxy/settings` sets `acme_email` (expiry notices; required for ZeroSSL), `acme_ca` (empty for Let's Encrypt, `letsencrypt_staging` for tests, or `zerossl`) and `trusted_proxies` (`cloudflare` trusts Cloudflare's published edge ranges, so IP filters and logs see the visitor's address). Switching versions and changing settings needs `proxy.update` and is recorded in the audit log; users without it see the ACME email with its local part hidden.

## TLS modes

| Mode | Label in the UI | Behaviour |
|---|---|---|
| `auto` | **Let's Encrypt** | Caddy obtains a certificate over ACME. The host name must resolve to this server and port 80 must be reachable from the internet. |
| `internal` | **Internal CA** | Caddy issues a certificate from its own local CA. Clients need to trust Caddy's root. |
| `none` | **HTTP only** | No automation policy is set for the host. |

<!-- update after: proxy: keep the Caddy container, resume routes, serve tls none as http -->
`none` doesn't give you a plain-HTTP site. All routes share one Caddy server on `:80` and `:443` with automatic HTTPS, so Caddy still tries to get a certificate for a `none` host and redirects HTTP to HTTPS. If TLS terminates in front of Dockmesh (a load balancer or tunnel), point that at your service directly rather than through a `none` route.

DNS-01 challenges and wildcard certificates can't be configured, even though the hostname hint mentions them. You also can't upload your own certificate.

## Certificates and ACME log

`GET /api/v1/proxy/routes` probes each route on port 443 and returns `cert_issuer`, `cert_valid_from`, `cert_valid_to` and `cert_days_left`. The UI doesn't show these fields.

The **ACME activity** card on the **Proxy** page lists certificate events (obtain, renew, failure) that Dockmesh reads from the container's log. It keeps the last 200 events in memory, and the list is empty after a server restart. For the full log, run `docker logs dockmesh-proxy`.

Per-route traffic metrics stay empty, because Caddy's metrics aren't enabled in the config Dockmesh pushes.

## Using your own proxy instead

Leave the proxy disabled, publish your services' ports in `compose.yaml` bound to loopback (`127.0.0.1:<port>:<port>`) when your proxy runs on the same host, and point your proxy at them. Dockmesh doesn't need to know about it.

See also [Ports](../reference/ports.md) and [Security hardening](../operations/hardening.md).
