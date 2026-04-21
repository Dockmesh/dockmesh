# v1 launch checklist

Everything that has to happen — in order — before
`curl -fsSL https://get.dockmesh.dev | bash` works for the general
public. Stuff in **BOLD** requires manual intervention from the
owner; everything else is automated.

## 1. **Create the `dockmesh` GitHub organisation**

1. Go to https://github.com/organizations/new
2. Pick the Free plan
3. Owner account: BlinkMSP (you)
4. Org name: `dockmesh`
5. Contact email: whatever you want public on the org profile
6. Finish the wizard — don't invite anyone yet, don't create repos

## 2. **Transfer the repo BlinkMSP/dockmesh → dockmesh/dockmesh**

1. https://github.com/BlinkMSP/dockmesh/settings → scroll to bottom
2. "Transfer ownership" → new owner: `dockmesh`
3. Confirm with repo name
4. **After transfer**: old URL auto-redirects for ~1 year, so pre-
   existing git clones keep working without intervention.
5. **CI tokens**: the release workflow uses the built-in
   `GITHUB_TOKEN`, no secret migration needed.

## 3. **Cut the first release** (v0.1.0)

```bash
git tag v0.1.0
git push origin v0.1.0
```

That fires `.github/workflows/release.yml` which builds the
amd64 + arm64 tarballs and publishes them to the GitHub Release.
Check progress at
https://github.com/dockmesh/dockmesh/actions.

When it's green, verify the release page has:

- `dockmesh_linux_amd64.tar.gz`
- `dockmesh_linux_arm64.tar.gz`
- `checksums.txt`

## 4. **Deploy the get.dockmesh.dev Worker**

```bash
cd C:\Dev\dockmesh.dev\workers\get
npm install
npx wrangler deploy
```

The first deploy asks you to log in to Cloudflare via browser. After
that it's `wrangler deploy` each time you change `src/index.ts` —
the script itself lives in the Dockmesh repo, so editing install.sh
does NOT require a worker redeploy.

## 5. **Bind DNS: `get.dockmesh.dev` → Worker route**

In Cloudflare dashboard:
1. DNS tab for `dockmesh.dev`
2. Add record: `get` / AAAA / `100::` / proxied (orange cloud)
   *(AAAA to 100:: is the Cloudflare trick for "this is a worker, no
   real origin" — the proxied flag routes it through CF's edge
   network so the worker's `routes: [{ pattern: "get.dockmesh.dev/*" }]`
   catches it.)*
3. Save

Verify:
```bash
curl -fsSL https://get.dockmesh.dev | head -10
# → should print the bash banner ASCII art
```

## 6. Smoke test end-to-end

On a fresh Linux VM (or a scratch container):

```bash
curl -fsSL https://get.dockmesh.dev | bash
# → binary lands in /usr/local/bin/dockmesh

sudo dockmesh init --yes
# → walks the wizard with defaults, prints the generated admin password

sudo systemctl enable --now dockmesh
# → service up on :8080
```

## 7. Update marketing copy

Once step 6 passes on a fresh VM, the dockmesh.dev landing page
can claim `curl | bash` installability with a clean conscience.

---

## What happens on every subsequent release

Bump the tag, push:

```bash
git tag v0.1.1
git push origin v0.1.1
```

- Release workflow builds + publishes tarballs.
- `get.dockmesh.dev` worker still serves the same `install.sh`.
- `install.sh` resolves `latest` via GitHub API — new users get
  the new version automatically.
- Old users can pin with
  `DOCKMESH_VERSION=v0.1.0 curl -fsSL … | bash`.
