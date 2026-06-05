<script setup lang="ts">
// HealthGated wraps an affordance the brain would refuse while a health issue is
// active (HEALTH.md # Display: "disabled action affordances with explanatory
// tooltips"; issue #12). When the matching blocks_* gate is set it renders the
// slot non-interactive (pointer-events off, dimmed) inside a tooltip wrapper
// that explains why ("Disabled because: <summary>"); otherwise it renders the
// slot untouched, so the common healthy case has zero layout cost. This
// standardizes the disable-with-reason pattern — a call site is just
// <HealthGated blocks="apps"><InstallButton /></HealthGated>. It mirrors the
// brain-side block (a click still can't mutate: blocks_apps also makes
// POST /api/v1/apps return 409), so this is UX, not the security boundary.
import { computed } from "vue";
import { useHealth } from "../useHealth";

const props = defineProps<{ blocks: "apps" | "writes" | "users" }>();

const { activeIssues } = useHealth();

const blockingIssue = computed(() =>
  activeIssues.value.find((i) =>
    props.blocks === "apps" ? i.blocks_apps : props.blocks === "writes" ? i.blocks_writes : i.blocks_users,
  ),
);
const blocked = computed(() => blockingIssue.value !== undefined);
const reason = computed(() =>
  blockingIssue.value ? `Disabled because: ${blockingIssue.value.summary}` : "",
);
</script>

<template>
  <span v-if="blocked" class="inline-block cursor-not-allowed" :title="reason" :aria-disabled="true">
    <span class="pointer-events-none block opacity-50">
      <slot />
    </span>
  </span>
  <slot v-else />
</template>
