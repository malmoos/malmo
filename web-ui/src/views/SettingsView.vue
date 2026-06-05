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
import { computed, ref } from "vue";
import { RouterLink } from "vue-router";
import { useQuery, useMutation, useQueryClient } from "@tanstack/vue-query";
import { SwitchRoot, SwitchThumb } from "reka-ui";
import { api, waitForJob, type Instance, type Job } from "../api";
import { useAuth } from "../auth";
import { useNotificationMutes } from "../useNotificationMutes";
import AppLogs from "../components/AppLogs.vue";

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

// Per-category notification mutes (NOTIFICATIONS.md # Configuration). The toggle
// shows "receiving" (on) = not muted, so an empty mute set reads as everything-on.
const { mutes, setMuted } = useNotificationMutes();
const mutedSet = computed(() => new Set(mutes.data.value?.muted ?? []));

// Display metadata for the notification taxonomy. The ids and their order mirror
// the wire contract (`notify.Categories`: storage | system | updates | security
// | account | app); the brain owns the taxonomy, the UI owns the wording. A new
// brain category needs a row here or it won't get a toggle.
const notificationCategories = [
  { id: "storage", label: "Storage", description: "Drives, free space, and data safety." },
  { id: "system", label: "System", description: "Startup, services, and background health." },
  { id: "updates", label: "Updates", description: "Available and installed updates." },
  { id: "security", label: "Security", description: "Sign-ins and account safety." },
  { id: "account", label: "Account", description: "Changes to people and access." },
  { id: "app", label: "Apps", description: "Activity and problems from your apps." },
];

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
    <!-- Management routes (DASHBOARD.md # global navigation). Activity is open to
         all users — members see only their own events (LOGGING.md # Visibility
         rules); Users is admin-only. -->
    <section class="space-y-2">
      <h2 class="text-xs font-medium uppercase tracking-wide text-muted-foreground">Administration</h2>
      <RouterLink
        to="/settings/activity"
        class="flex items-center justify-between rounded-xl border border-border bg-card px-4 py-3 hover:bg-muted"
      >
        <span class="text-sm font-medium">Activity</span>
        <span class="text-xs text-muted-foreground">→</span>
      </RouterLink>
      <RouterLink
        v-if="currentUser?.role === 'admin'"
        to="/settings/users"
        class="flex items-center justify-between rounded-xl border border-border bg-card px-4 py-3 hover:bg-muted"
      >
        <span class="text-sm font-medium">Users</span>
        <span class="text-xs text-muted-foreground">→</span>
      </RouterLink>
    </section>

    <section class="space-y-3">
      <h2 class="text-xs font-medium uppercase tracking-wide text-muted-foreground">Account</h2>
      <div class="rounded-xl border border-border bg-card px-4 py-3">
        <div class="text-sm font-medium">{{ currentUser?.username }}</div>
        <div class="text-xs capitalize text-muted-foreground">{{ currentUser?.role }}</div>
      </div>
    </section>

    <section class="space-y-3">
      <h2 class="text-xs font-medium uppercase tracking-wide text-muted-foreground">Notifications</h2>
      <p class="text-sm text-muted-foreground">Choose which kinds of notifications reach you. Turn a kind off to stop it from showing in your bell.</p>
      <p v-if="mutes.isLoading.value" class="text-sm text-muted-foreground">Loading…</p>
      <ul v-else class="space-y-2">
        <li
          v-for="c in notificationCategories"
          :key="c.id"
          class="flex items-center justify-between gap-4 rounded-xl border border-border bg-card px-4 py-3"
        >
          <div class="min-w-0">
            <div class="text-sm font-medium">{{ c.label }}</div>
            <div class="text-xs text-muted-foreground">{{ c.description }}</div>
          </div>
          <SwitchRoot
            :model-value="!mutedSet.has(c.id)"
            :aria-label="`${c.label} notifications`"
            class="relative inline-flex h-5 w-9 shrink-0 cursor-pointer items-center rounded-full border border-border bg-muted outline-none transition-colors data-[state=checked]:border-accent data-[state=checked]:bg-accent"
            @update:model-value="(on: boolean) => setMuted.mutate({ category: c.id, muted: !on })"
          >
            <SwitchThumb
              class="pointer-events-none block size-4 translate-x-0.5 rounded-full bg-card shadow transition-transform data-[state=checked]:translate-x-[1.125rem]"
            />
          </SwitchRoot>
        </li>
      </ul>
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
  </div>
</template>
