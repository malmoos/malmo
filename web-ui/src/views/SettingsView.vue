<script setup lang="ts">
// Settings — box + account settings, and the home for role-gated routes
// (DASHBOARD.md # global navigation). Most of that surface (Users, Activity,
// Storage, network) isn't built yet, so this view currently carries the account
// card plus a temporary "Installed apps" management list.
//
// Why uninstall lives here for now: Home tiles only *open* apps, and there's no
// per-app detail page yet — so the only place to uninstall is here. When an app
// detail page lands, per-instance management (uninstall, restart, settings)
// moves there and this section goes away. Uninstall authorization is enforced
// by the brain (members may only uninstall their own personal instances).
import { useQuery, useMutation, useQueryClient } from "@tanstack/vue-query";
import { api, waitForJob, type Instance, type Job } from "../api";
import { useAuth } from "../auth";

const { currentUser, singleUserMode } = useAuth();
const qc = useQueryClient();

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
  <div class="space-y-8 pt-2">
    <section class="space-y-3">
      <h2 class="text-xs font-medium uppercase tracking-wide text-muted-foreground">Account</h2>
      <div class="rounded-xl border border-border bg-card px-4 py-3">
        <div class="text-sm font-medium">{{ currentUser?.username }}</div>
        <div class="text-xs capitalize text-muted-foreground">{{ currentUser?.role }}</div>
      </div>
    </section>

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
          class="flex items-center justify-between rounded-xl border border-border bg-card px-4 py-3"
        >
          <div class="flex items-baseline gap-2">
            <strong class="text-sm">{{ a.name }}</strong>
            <span v-if="!singleUserMode" class="text-xs text-muted-foreground">{{ a.scope === "household" ? "Shared" : a.owner_username }}</span>
            <span class="text-xs text-muted-foreground">· {{ a.state }}</span>
          </div>
          <button
            class="rounded-lg border border-border px-3 py-1.5 text-sm hover:bg-muted disabled:opacity-50"
            :disabled="uninstall.isPending.value"
            @click="uninstall.mutate(a.id)"
          >
            Uninstall
          </button>
        </li>
      </ul>
    </section>
  </div>
</template>
