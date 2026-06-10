<script setup lang="ts">
// Settings → Installed apps → one instance. The per-app management page: header
// (logo, name, description), the action row (Open / Stop·Start / Uninstall),
// and the logs at the bottom. Rendered inside the Settings shell as a nested
// route (/settings/apps/:id), so the left nav stays put.
//
// Two data sources: GET /apps/{id} for the live instance (state, scope, url),
// and GET /catalog/{manifest_id} for the logo + description. The catalog lookup
// is best-effort — a Door-2 (custom) app has no catalog entry, so it falls back
// to the generic glyph and no description.
import { computed, ref, watch } from "vue";
import { useRoute, useRouter, RouterLink } from "vue-router";
import { useQuery, useMutation, useQueryClient } from "@tanstack/vue-query";
import { AppWindow, ChevronDown } from "lucide-vue-next";
import { api, waitForJob, type Instance, type CatalogDetail, type Job } from "@/api";
import { useAuth } from "@/auth";
import AppLogs from "@/components/AppLogs.vue";

const route = useRoute();
const router = useRouter();
const qc = useQueryClient();
const { currentUser } = useAuth();

const id = computed(() => String(route.params.id));

const appQuery = useQuery({
  queryKey: computed(() => ["apps", id.value]),
  queryFn: () => api.get<Instance>(`/apps/${id.value}`),
});
const app = computed(() => appQuery.data.value ?? null);

// Catalog lookup for logo + description. Best-effort: disabled until we know the
// manifest id, no retry (a Door-2 app legitimately 404s here), and any failure
// just leaves the glyph + "No description" fallback in place.
const catalogQuery = useQuery({
  queryKey: computed(() => ["catalog", app.value?.manifest_id]),
  queryFn: () => api.get<CatalogDetail>(`/catalog/${app.value!.manifest_id}`),
  enabled: computed(() => !!app.value?.manifest_id),
  retry: false,
});
const detail = computed(() => catalogQuery.data.value ?? null);

const brokenIcon = ref(false);
watch(id, () => { brokenIcon.value = false; });

// Authorization mirrors the brain: admins control any app; a member controls
// (and reads logs of) only their own personal instance. The list is already
// server-scoped, so a member never lands here for an app they can't see.
const canControl = computed(
  () => currentUser.value?.role === "admin" || app.value?.scope === "personal",
);

const running = computed(() => app.value?.state === "running");
const stopped = computed(() => app.value?.state === "stopped");

function invalidate() {
  qc.invalidateQueries({ queryKey: ["apps"] });
  qc.invalidateQueries({ queryKey: ["apps", id.value] });
}

// awaitJob polls to terminal and throws on a failed job, so a job-level failure
// (e.g. compose up never goes healthy) surfaces via useMutation's isError —
// api.post only throws on the synchronous 4xx, not on a job that fails later.
async function awaitJob(job: Job): Promise<Job> {
  const done = await waitForJob(job.job_id);
  if (done.status === "failed") throw new Error(done.error?.message || "the operation failed");
  return done;
}

const stop = useMutation({
  mutationFn: async () => awaitJob(await api.post<Job>(`/apps/${id.value}/stop`)),
  onSettled: invalidate,
});

const start = useMutation({
  mutationFn: async () => awaitJob(await api.post<Job>(`/apps/${id.value}/start`)),
  onSettled: invalidate,
});

// Uninstall is destructive, so it's a two-step inline confirm rather than a bare
// button. On success we leave the now-dead detail page for the list.
const confirmingUninstall = ref(false);

// Logs start collapsed — they're a drill-down, not the first thing on the page.
const logsOpen = ref(false);
const uninstall = useMutation({
  mutationFn: async () => awaitJob(await api.del<Job>(`/apps/${id.value}`)),
  onSuccess: () => {
    qc.invalidateQueries({ queryKey: ["apps"] });
    router.push("/settings/apps");
  },
});

const busy = computed(
  () => stop.isPending.value || start.isPending.value || uninstall.isPending.value,
);
</script>

<template>
  <div class="flex h-full flex-col gap-8 pt-2">
    <RouterLink to="/settings/apps" class="inline-block text-sm text-muted-foreground hover:text-foreground">
      ← Installed apps
    </RouterLink>

    <p v-if="appQuery.isLoading.value" class="text-sm text-muted-foreground">Loading…</p>
    <p v-else-if="appQuery.isError.value" class="text-sm text-destructive">
      Couldn't load this app. {{ (appQuery.error.value as Error)?.message }}
    </p>

    <template v-else-if="app">
      <!-- Header: logo · name/description · state -->
      <header class="flex flex-col gap-5 sm:flex-row sm:items-center">
        <div
          class="grid size-20 shrink-0 place-items-center overflow-hidden rounded-3xl border border-border bg-card text-muted-foreground"
        >
          <img
            v-if="detail?.icon_url && !brokenIcon"
            :src="detail.icon_url"
            :alt="`${app.name} icon`"
            class="size-full object-cover"
            @error="brokenIcon = true"
          />
          <AppWindow v-else class="size-9" />
        </div>

        <div class="min-w-0 flex-1">
          <h1 class="text-xl font-semibold">{{ app.name }}</h1>
          <p v-if="detail?.short_description" class="mt-0.5 text-sm text-muted-foreground">
            {{ detail.short_description }}
          </p>
          <p class="mt-1 text-xs uppercase tracking-wide text-muted-foreground">{{ app.state }}</p>
        </div>
      </header>

      <!-- Action row -->
      <section class="flex flex-wrap items-center gap-2">
        <a
          v-if="running"
          :href="app.url"
          target="_blank"
          rel="noopener"
          class="rounded-lg bg-accent px-4 py-1.5 text-sm font-medium text-accent-foreground hover:opacity-90"
        >
          Open
        </a>

        <button
          v-if="canControl && running"
          class="rounded-lg border border-border px-3 py-1.5 text-sm hover:bg-muted disabled:opacity-50"
          :disabled="busy"
          @click="stop.mutate()"
        >
          {{ stop.isPending.value ? "Stopping…" : "Stop service" }}
        </button>
        <button
          v-else-if="canControl && stopped"
          class="rounded-lg border border-border px-3 py-1.5 text-sm hover:bg-muted disabled:opacity-50"
          :disabled="busy"
          @click="start.mutate()"
        >
          {{ start.isPending.value ? "Starting…" : "Start service" }}
        </button>

        <template v-if="canControl">
          <template v-if="confirmingUninstall">
            <span class="text-sm text-muted-foreground">Uninstall {{ app.name }}? This deletes its data.</span>
            <button
              class="rounded-lg border border-destructive/40 bg-destructive/10 px-3 py-1.5 text-sm text-destructive hover:bg-destructive/20 disabled:opacity-50"
              :disabled="busy"
              @click="uninstall.mutate()"
            >
              {{ uninstall.isPending.value ? "Uninstalling…" : "Confirm uninstall" }}
            </button>
            <button
              class="rounded-lg border border-border px-3 py-1.5 text-sm hover:bg-muted"
              :disabled="busy"
              @click="confirmingUninstall = false"
            >
              Cancel
            </button>
          </template>
          <button
            v-else
            class="rounded-lg border border-border px-3 py-1.5 text-sm hover:bg-muted disabled:opacity-50"
            :disabled="busy"
            @click="confirmingUninstall = true"
          >
            Uninstall
          </button>
        </template>
      </section>

      <!-- Action error surface (job failure / 409 / host 5xx) -->
      <p v-if="stop.isError.value" class="text-sm text-destructive">
        Couldn't stop: {{ (stop.error.value as Error)?.message }}
      </p>
      <p v-if="start.isError.value" class="text-sm text-destructive">
        Couldn't start: {{ (start.error.value as Error)?.message }}
      </p>
      <p v-if="uninstall.isError.value" class="text-sm text-destructive">
        Couldn't uninstall: {{ (uninstall.error.value as Error)?.message }}
      </p>

      <!-- Logs — collapsed by default; a full-width accordion row (styled like
           the Installed apps list rows) that expands the log panel on click. The
           chevron at the end rotates to signal expansion. -->
      <section
        v-if="canControl"
        class="flex flex-col gap-2"
        :class="{ 'min-h-0 flex-1': logsOpen }"
      >
        <button
          type="button"
          class="flex w-full shrink-0 items-center justify-between gap-3 rounded-xl border border-border bg-card px-4 py-3 text-sm hover:bg-muted"
          :aria-expanded="logsOpen"
          @click="logsOpen = !logsOpen"
        >
          <span class="font-medium">Logs</span>
          <ChevronDown class="size-4 shrink-0 text-muted-foreground transition-transform" :class="{ 'rotate-180': logsOpen }" />
        </button>
        <AppLogs v-if="logsOpen" :id="app.id" fill class="min-h-0 flex-1" />
      </section>
    </template>
  </div>
</template>
