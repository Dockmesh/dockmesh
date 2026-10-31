<script lang="ts">
  import type { NoticeSeverity } from '$lib/api';
  import { noticeLabel } from '$lib/notices';

  let { severity }: { severity: NoticeSeverity } = $props();
  const cls = { critical: 'dm-pill-danger', warning: 'dm-pill-warning', info: 'dm-pill-info' } as const;
</script>

<span class="dm-pill {cls[severity]} whitespace-nowrap"><span class="dm-pill-dot"></span>{noticeLabel[severity]}</span>
