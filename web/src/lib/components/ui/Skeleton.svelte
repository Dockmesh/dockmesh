<script lang="ts">
  interface Props {
    class?: string;
    width?: string;
    height?: string;
  }
  let { class: klass = '', width = '100%', height = '1rem' }: Props = $props();
</script>

<div class="dm-skeleton {klass}" style="width: {width}; height: {height};"></div>
