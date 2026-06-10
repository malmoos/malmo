<script setup lang="ts">
// Home = the app launcher (DASHBOARD.md # the home screen is the app launcher).
// A grid of tiles grouped into two sections:
//   - Household — shared instances the user is permitted to open.
//   - Yours — the current user's personal instances.
// The brain already scopes GET /apps per caller (household + own personal;
// admins additionally see others' personal). The home screen deliberately
// shows only Household + *my* Yours, so we filter personal tiles to the
// current user — other members' personal instances never appear here even for
// an admin (DASHBOARD.md: "they never see other members' personal instances").
import { computed, ref } from "vue";
import { RouterLink } from "vue-router";
import { useQuery, useQueryClient } from "@tanstack/vue-query";
import { api, waitForJob, type Instance, type Job } from "../api";
import { pushErrorToast } from "../toasts";
import { useAuth } from "../auth";
import { useHealth } from "../useHealth";
import { relativeTime } from "../utils";
import AppTile from "../components/AppTile.vue";

const { currentUser, singleUserMode } = useAuth();

// Active health issues (admin-only query; empty for members) — the banner's
// click target lands here (#health-issues). Lists every active issue, including
// warnings the global banner doesn't surface (HEALTH.md # Display).
const { activeIssues } = useHealth();

const apps = useQuery({
  queryKey: ["apps"],
  queryFn: () => api.get<{ apps: Instance[] }>("/apps"),
});

const all = computed(() => apps.data.value?.apps ?? []);
const household = computed(() => all.value.filter((a) => a.scope === "household"));
const yours = computed(() =>
  all.value.filter((a) => a.scope === "personal" && a.owner_user_id === currentUser.value?.id),
);
const empty = computed(() => household.value.length === 0 && yours.value.length === 0);

// Click-to-start straight from a stopped tile (DASHBOARD.md # Tile). The set of
// in-flight ids drives each tile's "Starting up…" caption; the brain re-checks
// authorization, so a tile only emits start when the viewer may control it.
const qc = useQueryClient();
const startingIds = ref(new Set<string>());

async function startApp(id: string, name: string) {
  if (startingIds.value.has(id)) return;
  startingIds.value = new Set(startingIds.value).add(id);
  try {
    const job = await api.post<Job>(`/apps/${id}/start`);
    const done = await waitForJob(job.job_id);
    if (done.status === "failed") throw new Error(done.error?.message || "the start job failed");
  } catch (e) {
    // The tile just falls back to its grayed stopped state, so without a toast a
    // failed start (403/409/job error) would be silent. The detail page surfaces
    // these inline; here a toast is the only feedback channel.
    pushErrorToast(`Couldn't start ${name}: ${(e as Error).message}`);
  } finally {
    const next = new Set(startingIds.value);
    next.delete(id);
    startingIds.value = next;
    qc.invalidateQueries({ queryKey: ["apps"] });
  }
}
</script>

<template>
  <div class="space-y-8 pt-2">
    <!-- Active health issues (HEALTH.md # Display, the inline issues list). The
         degraded-mode banner links here; shows the full active set, warnings
         included. Admin-only (the query is gated), so members never see it. -->
    <section v-if="activeIssues.length" id="health-issues" class="space-y-3">
      <h2 class="text-xs font-medium uppercase tracking-wide text-muted-foreground">System health</h2>
      <ul class="space-y-2">
        <li
          v-for="i in activeIssues"
          :key="`${i.id} ${i.instance_key ?? ''}`"
          class="flex items-start gap-3 rounded-xl border border-border bg-card px-4 py-3"
        >
          <span class="health-dot" :data-sev="i.severity" aria-hidden="true"></span>
          <div class="min-w-0 flex-1">
            <div class="text-sm">{{ i.summary }}</div>
            <div v-if="i.details" class="mt-0.5 text-xs text-muted-foreground">{{ i.details }}</div>
            <div class="mt-1 flex flex-wrap gap-x-2 text-xs text-muted-foreground">
              <span class="font-mono">{{ i.id }}<template v-if="i.instance_key">: {{ i.instance_key }}</template></span>
              <span aria-hidden="true">·</span>
              <span>raised {{ relativeTime(Date.parse(i.raised_at)) }}</span>
            </div>
          </div>
          <span class="health-sev" :data-sev="i.severity">{{ i.severity }}</span>
        </li>
      </ul>
    </section>

    <p v-if="apps.isLoading.value" class="text-sm text-muted-foreground">Loading…</p>

    <!-- First arrival / empty state: invite, don't shove (DASHBOARD.md). -->
    <div
      v-else-if="empty"
      class="mt-16 flex flex-col items-center gap-3 text-center text-muted-foreground"
    >
      <p>No apps yet.</p>
      <RouterLink
        to="/store"
        class="rounded-lg bg-accent px-4 py-2 text-sm font-medium text-accent-foreground"
      >
        Browse the Store
      </RouterLink>
    </div>

    <template v-else>
      <section v-if="household.length" class="space-y-3">
        <h2 v-if="!singleUserMode" class="text-xs font-medium uppercase tracking-wide text-muted-foreground">Household</h2>
        <div class="grid grid-cols-2 gap-x-6 gap-y-8 sm:grid-cols-4 lg:grid-cols-6">
          <AppTile v-for="a in household" :key="a.id" :instance="a" :starting="startingIds.has(a.id)" @start="startApp(a.id, a.name)" />
        </div>
      </section>

      <section v-if="yours.length" class="space-y-3">
        <h2 v-if="!singleUserMode" class="text-xs font-medium uppercase tracking-wide text-muted-foreground">Yours</h2>
        <div class="grid grid-cols-2 gap-x-6 gap-y-8 sm:grid-cols-4 lg:grid-cols-6">
          <AppTile v-for="a in yours" :key="a.id" :instance="a" :starting="startingIds.has(a.id)" @start="startApp(a.id, a.name)" />
        </div>
      </section>
    </template>
  </div>
</template>

<style scoped>
/* Severity palette shared with NotificationBell's dots (HEALTH.md severities). */
.health-dot {
  flex: 0 0 8px;
  width: 8px;
  height: 8px;
  margin-top: 0.4rem;
  border-radius: 999px;
  background: #adb5bd;
}
.health-dot[data-sev="warning"] { background: #f59f00; }
.health-dot[data-sev="error"] { background: #e8590c; }
.health-dot[data-sev="critical"] { background: #e03131; }
.health-sev {
  flex: 0 0 auto;
  align-self: center;
  padding: 0.05rem 0.45rem;
  border-radius: 999px;
  font-size: 0.62rem;
  font-weight: 700;
  text-transform: uppercase;
  letter-spacing: 0.04em;
  color: #fff;
  background: #adb5bd;
}
.health-sev[data-sev="warning"] { background: #f59f00; }
.health-sev[data-sev="error"] { background: #e8590c; }
.health-sev[data-sev="critical"] { background: #e03131; }
</style>
