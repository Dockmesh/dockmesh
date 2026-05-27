<script lang="ts">
  import {
    EditorialPage,
    Eyebrow,
    StatusPill,
    Sparkline,
    Field,
    EdRow,
    EdMetric,
    EditorialModal,
  } from '$lib/components/editorial';

  let modalOpen = $state(false);
  let typed = $state('');
  let demoTags = $state(['edge', 'europe', 'backup']);

  // Synthetic time series for sparkline demos.
  const cpuSeries = [22, 19, 23, 31, 28, 25, 38, 41, 36, 30, 27, 34, 42, 38, 35, 31, 28, 30, 34];
  const memSeries = [11.2, 11.4, 11.6, 11.8, 12.1, 12.3, 12.4, 12.4, 12.3, 12.4, 12.4, 12.4];
  const reqSeries = [120, 145, 132, 178, 210, 198, 220, 245, 210, 198, 215, 240, 232, 248, 261, 244, 232, 250];

  const STACKS = [
    { id: 'audiobookshelf', host: 'edge-fra-1', services: 5, status: 'running', updated: '2h ago', ver: 'v2.17.4' },
    { id: 'monitoring',     host: 'edge-fra-1', services: 6, status: 'degraded', updated: '18m ago', ver: 'v2024.10' },
    { id: 'media',          host: 'node-ber-2', services: 6, status: 'running', updated: '1d ago', ver: 'v3.2.1' },
    { id: 'paperless',      host: 'node-ams-3', services: 4, status: 'stopped', updated: '3d ago', ver: 'v2.13.0' },
    { id: 'n8n',            host: 'node-ams-3', services: 3, status: 'failing', updated: '44m ago', ver: 'v1.62.1' },
  ] as const;
</script>

<EditorialPage>
  <div class="design-frame">
    <header class="design-hero">
      <Eyebrow active>Design system</Eyebrow>
      <h1 class="ed-title">Editorial <em>primitives</em>, slice 1.</h1>
      <p class="ed-subtitle">
        Foundation for the frontend modernization. Every primitive on this page
        is also exported from <code>$lib/components/editorial</code>. Convert
        a page by composing these — never inline the styles.
      </p>
    </header>

    <hr class="ed-rule" />

    <!-- ──────────────────────────────────────────────────────── Eyebrow -->
    <section class="design-section">
      <Eyebrow>Eyebrow</Eyebrow>
      <h2 class="ed-section-title">Mono uppercase caption with optional cursor</h2>
      <div class="design-row">
        <Eyebrow>Overview</Eyebrow>
        <Eyebrow active>Sign in</Eyebrow>
        <Eyebrow>Stacks · 12</Eyebrow>
      </div>
    </section>

    <!-- ──────────────────────────────────────────────────────── Status pills -->
    <section class="design-section">
      <Eyebrow>StatusPill</Eyebrow>
      <h2 class="ed-section-title">Flat, line-art, one dot + one word</h2>
      <div class="design-row">
        <StatusPill status="running" />
        <StatusPill status="ok" />
        <StatusPill status="degraded" />
        <StatusPill status="warn" />
        <StatusPill status="failing" />
        <StatusPill status="stopped" />
        <StatusPill status="pending" />
        <StatusPill status="invited" />
        <StatusPill status="neutral" label="custom" />
      </div>
    </section>

    <!-- ──────────────────────────────────────────────────────── Metrics -->
    <section class="design-section">
      <Eyebrow>EdMetric · 4-up grid</Eyebrow>
      <h2 class="ed-section-title">No big colored stat-cards — sparkline + label + meta</h2>
      <div class="design-grid-4">
        <EdMetric label="CPU · fleet avg" value="41" unit="%" meta="↗ +9% / 1h" spark={cpuSeries} />
        <EdMetric label="Memory" value="21.5" unit="/ 32 GB" meta="67% across 3 hosts" spark={memSeries} />
        <EdMetric label="Requests / min" value="248" meta="last 60s" spark={reqSeries} />
        <EdMetric
          label="Stacks"
          value="7 / 10"
          meta="2 need attention"
          spark={[10, 10, 10, 9, 9, 8, 8]}
          sparkColor="var(--color-warning-500)"
        />
      </div>
    </section>

    <!-- ──────────────────────────────────────────────────────── EdRow list -->
    <section class="design-section">
      <Eyebrow>EdRow · status-stripe list</Eyebrow>
      <h2 class="ed-section-title">2px left stripe, no full-row tint</h2>
      <div class="dm-card" style="overflow: hidden;">
        {#each STACKS as s (s.id)}
          <EdRow
            status={s.status}
            href="#stack-{s.id}"
            columns="6px minmax(0, 2fr) 1.2fr 1fr 0.8fr auto"
          >
            <span class="design-row-name">
              <span class="design-row-id">{s.id}</span>
              <span class="design-row-meta">{s.services} svc · {s.ver}</span>
            </span>
            <span class="design-row-mono">{s.host}</span>
            <StatusPill status={s.status} />
            <span class="design-row-mono">{s.updated}</span>
            <span class="design-row-arrow" aria-hidden="true">→</span>
          </EdRow>
        {/each}
      </div>
    </section>

    <!-- ──────────────────────────────────────────────────────── Tabs + table -->
    <section class="design-section">
      <Eyebrow>Tabs + ed-table</Eyebrow>
      <h2 class="ed-section-title">Underline-on-active, hairline rows</h2>

      <div class="ed-tabs">
        <button class="ed-tab active">All <span class="count">12</span></button>
        <button class="ed-tab">Running <span class="count">7</span></button>
        <button class="ed-tab">Needs attention <span class="count">2</span></button>
        <button class="ed-tab">Stopped <span class="count">1</span></button>
      </div>

      <div class="dm-card" style="margin-top: 18px; overflow: hidden;">
        <table class="ed-table">
          <thead>
            <tr>
              <th>Service</th>
              <th>Image</th>
              <th>Replicas</th>
              <th>Status</th>
            </tr>
          </thead>
          <tbody>
            <tr>
              <td>prometheus</td>
              <td class="col-num">prom/prometheus:v2.54.1</td>
              <td class="col-num">1/1</td>
              <td><StatusPill status="running" /></td>
            </tr>
            <tr>
              <td>grafana</td>
              <td class="col-num">grafana/grafana:11.2.2</td>
              <td class="col-num">1/1</td>
              <td><StatusPill status="running" /></td>
            </tr>
            <tr>
              <td><em class="ed-accent">loki</em></td>
              <td class="col-num">grafana/loki:3.2.0</td>
              <td class="col-num">0/1</td>
              <td><StatusPill status="degraded" /></td>
            </tr>
          </tbody>
        </table>
      </div>
    </section>

    <!-- ──────────────────────────────────────────────────────── Field + ed-input -->
    <section class="design-section">
      <Eyebrow>Field + ed-input</Eyebrow>
      <h2 class="ed-section-title">Bottom-bordered inputs with mono uppercase labels</h2>
      <div class="design-grid-2">
        <Field label="Hostname">
          <input class="ed-input ed-input-mono" value="node-zur-4" />
        </Field>
        <Field
          label="Description"
          hint="Optional. Shows up next to the host id in the dashboard."
        >
          <input class="ed-input" placeholder="Backup node in Zürich datacenter" />
        </Field>
      </div>
    </section>

    <!-- ──────────────────────────────────────────────────────── Sparkline -->
    <section class="design-section">
      <Eyebrow>Sparkline · stand-alone</Eyebrow>
      <h2 class="ed-section-title">SVG, no chart-library dependency</h2>
      <div class="design-spark-grid">
        <div>
          <span class="ed-metric-label">brand accent</span>
          <Sparkline values={cpuSeries} />
        </div>
        <div>
          <span class="ed-metric-label">warn override</span>
          <Sparkline values={cpuSeries} color="var(--color-warning-500)" />
        </div>
        <div>
          <span class="ed-metric-label">success override</span>
          <Sparkline values={cpuSeries} color="var(--color-success-500)" />
        </div>
      </div>
    </section>

    <!-- ──────────────────────────────────────────────────────── Bars -->
    <section class="design-section">
      <Eyebrow>dm-bar · resource bars</Eyebrow>
      <h2 class="ed-section-title">Thin, line-art, threshold colours</h2>
      <div class="design-bars">
        <div>
          <div class="design-bar-row"><span class="ed-metric-label">CPU</span><span class="design-row-mono">34%</span></div>
          <div class="dm-bar"><span class="dm-bar-fill" style="width: 34%"></span></div>
        </div>
        <div>
          <div class="design-bar-row"><span class="ed-metric-label">Memory</span><span class="design-row-mono">71% — 11.4 / 16 GB</span></div>
          <div class="dm-bar"><span class="dm-bar-fill dm-bar-fill--warn" style="width: 71%"></span></div>
        </div>
        <div>
          <div class="design-bar-row"><span class="ed-metric-label">Disk</span><span class="design-row-mono">92% — 920 / 1000 GB</span></div>
          <div class="dm-bar"><span class="dm-bar-fill dm-bar-fill--danger" style="width: 92%"></span></div>
        </div>
      </div>
    </section>

    <!-- ──────────────────────────────────────────────────────── Activity feed -->
    <section class="design-section">
      <Eyebrow>ed-feed · activity</Eyebrow>
      <h2 class="ed-section-title">Time · text · actor — three columns, hairline rows</h2>
      <div class="dm-card" style="padding: 4px 16px;">
        <div class="ed-feed-item">
          <span class="ed-feed-time">18m</span>
          <span class="ed-feed-text">
            <strong>monitoring</strong> deployed — <em class="ed-accent">loki</em> failed health-check after rollout
          </span>
          <span class="ed-feed-actor">tobias</span>
        </div>
        <div class="ed-feed-item">
          <span class="ed-feed-time">44m</span>
          <span class="ed-feed-text"><strong>n8n</strong> exited (1) — restart loop, 4 attempts</span>
          <span class="ed-feed-actor">tobias</span>
        </div>
        <div class="ed-feed-item">
          <span class="ed-feed-time">2h</span>
          <span class="ed-feed-text"><strong>audiobookshelf</strong> updated to <code>v2.17.4</code></span>
          <span class="ed-feed-actor">watchtower</span>
        </div>
      </div>
    </section>

    <!-- ──────────────────────────────────────────────────────── Logs -->
    <section class="design-section">
      <Eyebrow>log-viewer</Eyebrow>
      <h2 class="ed-section-title">Pre-styled mono block, per-level colours</h2>
      <div class="log-viewer">
        <div class="log-line">
          <span class="log-time">10:14:02.119</span>
          <span class="log-svc">loki</span>
          <span class="log-msg">level=info ts=2026-05-03T10:14:02Z msg="server listening on" addr=:3100</span>
        </div>
        <div class="log-line">
          <span class="log-time">10:14:02.890</span>
          <span class="log-svc">loki</span>
          <span class="log-msg log-msg--warn">level=warn component=ingester msg="checkpoint older than expected" age=14m</span>
        </div>
        <div class="log-line">
          <span class="log-time">10:14:03.512</span>
          <span class="log-svc">loki</span>
          <span class="log-msg log-msg--err">level=error msg="failed to connect to compactor" err="dial tcp: lookup compactor: i/o timeout"</span>
        </div>
        <div class="log-line">
          <span class="log-time">10:14:04.020</span>
          <span class="log-svc">prometheus</span>
          <span class="log-msg log-msg--ok">level=info msg="WAL replay completed" duration=0.8s</span>
        </div>
      </div>
    </section>

    <!-- ──────────────────────────────────────────────────────── Modal demo -->
    <section class="design-section">
      <Eyebrow>EditorialModal</Eyebrow>
      <h2 class="ed-section-title">Hairline border, no shadow, eyebrow + italic-accent title</h2>
      <button class="dm-btn dm-btn-secondary" onclick={() => (modalOpen = true)}>
        Open destroy-confirmation modal
      </button>

      <EditorialModal
        bind:open={modalOpen}
        eyebrow="Confirm · destructive"
        width={520}
      >
        {#snippet title()}
          Delete <em>monitoring</em>?
        {/snippet}

        <p class="design-modal-body">
          This stops all <em class="ed-accent">6 services</em>, removes their containers,
          deletes the volumes <code>prom-data</code>, <code>loki-data</code>,
          <code>grafana-data</code>, and forgets the compose history.
          <em class="ed-accent">This cannot be undone.</em>
        </p>

        <div style="margin-top: 18px;">
          <Field label="Type monitoring below to confirm">
            <input class="ed-input ed-input-mono" bind:value={typed} placeholder="monitoring" />
          </Field>
        </div>

        {#snippet footer()}
          <span class="ed-feed-time"><span class="ed-kbd">Esc</span> to cancel</span>
          <span style="display: inline-flex; gap: 8px;">
            <button class="dm-btn dm-btn-ghost dm-btn-sm" onclick={() => (modalOpen = false)}>Cancel</button>
            <button class="dm-btn dm-btn-danger dm-btn-sm" disabled={typed !== 'monitoring'}>
              Delete monitoring
            </button>
          </span>
        {/snippet}
      </EditorialModal>
    </section>

    <!-- ──────────────────────────────────────────────────────── Token note -->
    <section class="design-section">
      <Eyebrow>Tokens · per-theme</Eyebrow>
      <h2 class="ed-section-title">Use the topbar moon/sun toggle to flip themes</h2>
      <div class="design-token-grid">
        <div class="design-token"><span style="background: var(--bg)"></span><code>--bg</code></div>
        <div class="design-token"><span style="background: var(--bg-elevated)"></span><code>--bg-elevated</code></div>
        <div class="design-token"><span style="background: var(--surface)"></span><code>--surface</code></div>
        <div class="design-token"><span style="background: var(--surface-hover)"></span><code>--surface-hover</code></div>
        <div class="design-token"><span style="background: var(--border)"></span><code>--border</code></div>
        <div class="design-token"><span style="background: var(--border-strong)"></span><code>--border-strong</code></div>
        <div class="design-token"><span style="background: var(--accent)"></span><code>--accent</code></div>
        <div class="design-token"><span style="background: var(--color-success-500)"></span><code>success-500</code></div>
        <div class="design-token"><span style="background: var(--color-warning-500)"></span><code>warning-500</code></div>
        <div class="design-token"><span style="background: var(--color-danger-500)"></span><code>danger-500</code></div>
      </div>
    </section>
  </div>
</EditorialPage>

<style>
  .design-frame {
    max-width: 1280px;
    padding: 36px 44px 96px;
  }
  .design-hero { margin-bottom: 28px; }
  .design-section { margin-top: 56px; }
  .design-section .ed-section-title {
    color: var(--fg-muted);
    margin-top: 6px;
    margin-bottom: 18px;
    font-weight: 400;
    font-size: 13px;
  }
  .design-row {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 14px;
    margin-top: 4px;
  }
  .design-grid-4 {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(180px, 1fr));
    gap: 12px;
  }
  .design-grid-2 {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(280px, 1fr));
    gap: 22px;
  }
  .design-row-name {
    display: flex;
    flex-direction: column;
    gap: 2px;
  }
  .design-row-id {
    font-size: 13px;
    color: var(--fg);
    font-weight: 500;
  }
  .design-row-meta {
    font-family: var(--font-mono);
    font-size: 10.5px;
    color: var(--fg-subtle);
  }
  .design-row-mono {
    font-family: var(--font-mono);
    font-size: 11px;
    color: var(--fg-muted);
  }
  .design-row-arrow {
    color: var(--fg-subtle);
    font-family: var(--font-mono);
  }
  .design-spark-grid {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(220px, 1fr));
    gap: 22px;
  }
  .design-spark-grid > div { display: flex; flex-direction: column; gap: 6px; }
  .design-bars {
    display: flex;
    flex-direction: column;
    gap: 18px;
  }
  .design-bar-row {
    display: flex;
    align-items: baseline;
    justify-content: space-between;
    margin-bottom: 6px;
  }
  .design-modal-body {
    font-size: 13.5px;
    color: var(--fg-muted);
    line-height: 1.6;
    margin: 0;
  }
  .design-token-grid {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(180px, 1fr));
    gap: 10px;
  }
  .design-token {
    display: flex;
    align-items: center;
    gap: 10px;
    padding: 8px 10px;
    border: 1px solid var(--border);
    border-radius: 6px;
  }
  .design-token > span {
    width: 22px;
    height: 22px;
    border: 1px solid var(--border-strong);
    border-radius: 3px;
    display: inline-block;
    flex-shrink: 0;
  }
  .design-token code {
    font-family: var(--font-mono);
    font-size: 11px;
    color: var(--fg-muted);
  }
</style>
