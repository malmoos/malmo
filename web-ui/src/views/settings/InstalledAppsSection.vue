<script setup lang="ts">
// Settings → Installed apps — the manage/uninstall/logs list. Extracted from the
// old single-page SettingsView when Settings became a left-nav shell
// (SettingsLayout.vue).
//
// Why uninstall lives here for now: Home tiles only *open* apps, and there's no
// per-app detail page yet — so the only place to uninstall is here. When an app
// detail page lands, per-instance management (uninstall, restart, settings) moves
// there and this section goes away. Uninstall authorization is enforced by the
// brain (members may only uninstall their own personal instances).
import { ref } from "vue";
import { useQuery, useMutation, useQueryClient } from "@tanstack/vue-query";
import { api, waitForJob, type Instance, type Job } from "@/api";
import { useAuth } from "@/auth";
import AppLogs from "@/components/AppLogs.vue";

const { currentUser, singleUserMode } = useAuth();
const qc = useQueryClient();

// Which app's Logs panel is expanded (one at a time, for a calm list). The Logs
// toggle is pre-gated to viewers the brain would allow: admins always, plus a
// member viewing their own personal app. The apps list is already
// visibility-scoped server-side, so a member only ever sees household apps (logs
// admins-only → no toggle) and their own personal apps (logs allowed).
const openLogsId = ref<string | null>(null);
function canViewLogs(a: Instance): boolean {
  return currentUser.value?.role === "admin" || a.scope === "personal";
}
function toggleLogs(id: string) {
  openLogsId.value = openLogsId.value === id ? null : id;
}

const apps = useQuery({
  queryKey: ["apps"],
  queryFn: () => api.get<{ apps: Instance[] }>("/apps"),
});

const uninstall = useMutation({
  mutationFn: async (id: string) => {
    const job = await api.del<Job>(`/apps/${id}`);
    return waitForJob(job.job_id);
  },
  onSettled: () => qc.invalidateQueries({ queryKey: ["apps"] }),
});
</script>

<template>
  <section class="space-y-3">
    <h2 class="text-xs font-medium uppercase tracking-wide text-muted-foreground">Installed apps</h2>
    <p v-if="apps.isLoading.value" class="text-sm text-muted-foreground">Loading…</p>
    <p
      v-else-if="(apps.data.value?.apps.length ?? 0) === 0"
      class="text-sm text-muted-foreground"
    >
      Nothing installed yet.
    </p>
    <ul v-else class="space-y-2">
      <li
        v-for="a in apps.data.value!.apps"
        :key="a.id"
        class="space-y-3 rounded-xl border border-border bg-card px-4 py-3"
      >
        <div class="flex items-center justify-between">
          <div class="flex items-baseline gap-2">
            <strong class="text-sm">{{ a.name }}</strong>
            <span v-if="!singleUserMode" class="text-xs text-muted-foreground">{{ a.scope === "household" ? "Shared" : a.owner_username }}</span>
            <span class="text-xs text-muted-foreground">· {{ a.state }}</span>
          </div>
          <div class="flex items-center gap-2">
            <button
              v-if="canViewLogs(a)"
              class="rounded-lg border border-border px-3 py-1.5 text-sm hover:bg-muted"
              :class="{ 'bg-muted': openLogsId === a.id }"
              @click="toggleLogs(a.id)"
            >
              Logs
            </button>
            <button
              class="rounded-lg border border-border px-3 py-1.5 text-sm hover:bg-muted disabled:opacity-50"
              :disabled="uninstall.isPending.value"
              @click="uninstall.mutate(a.id)"
            >
              Uninstall
            </button>
          </div>
        </div>
        <AppLogs v-if="openLogsId === a.id" :id="a.id" />
      </li>
    </ul>
  </section>
</template>
