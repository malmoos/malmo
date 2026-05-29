<script setup lang="ts">
import { ref } from "vue";
import { useQuery, useMutation, useQueryClient } from "@tanstack/vue-query";
import { api, waitForJob, type ApiError, type CatalogEntry, type Instance, type Job } from "./api";
import { useEvents } from "./useEvents";
import NotificationBell from "./NotificationBell.vue";
// Login/logout are intentionally hidden in v1 (single-user dev phase). The
// auth machinery is wired end-to-end (`logout`, `useAuth`, Login.vue) — we
// just don't surface the controls yet. Re-enable when multi-user lands.
useEvents();
const qc = useQueryClient();

const catalog = useQuery({
  queryKey: ["catalog"],
  queryFn: () => api.get<{ apps: CatalogEntry[] }>("/catalog"),
});

const apps = useQuery({
  queryKey: ["apps"],
  queryFn: () => api.get<{ apps: Instance[] }>("/apps"),
});

const install = useMutation({
  mutationFn: async (manifestId: string) => {
    const job = await api.post<Job>("/apps", { manifest_id: manifestId });
    return waitForJob(job.job_id);
  },
  onSettled: () => qc.invalidateQueries({ queryKey: ["apps"] }),
});

const uninstall = useMutation({
  mutationFn: async (id: string) => {
    const job = await api.del<Job>(`/apps/${id}`);
    return waitForJob(job.job_id);
  },
  onSettled: () => qc.invalidateQueries({ queryKey: ["apps"] }),
});

function installedManifest(id: string): boolean {
  return (apps.data.value?.apps ?? []).some((a) => a.manifest_id === id);
}

const customName = ref("");
const customPort = ref<number | undefined>(undefined);
const customCompose = ref("");

const installCustom = useMutation({
  mutationFn: async () => {
    const job = await api.post<Job>("/apps/custom", {
      name: customName.value,
      compose: customCompose.value,
      main_port: Number(customPort.value),
    });
    return waitForJob(job.job_id);
  },
  onSuccess: () => {
    customName.value = "";
    customPort.value = undefined;
    customCompose.value = "";
  },
  onSettled: () => qc.invalidateQueries({ queryKey: ["apps"] }),
});
</script>

<template>
  <main>
    <header>
      <h1>malmo</h1>
      <span class="tag">dev dashboard</span>
      <NotificationBell class="header-bell" />
    </header>

    <section>
      <h2>Installed apps</h2>
      <p v-if="apps.isLoading.value" class="muted">Loading…</p>
      <p v-else-if="(apps.data.value?.apps.length ?? 0) === 0" class="muted">
        Nothing installed yet — install something from the catalog below.
      </p>
      <ul v-else class="cards">
        <li v-for="a in apps.data.value!.apps" :key="a.id" class="card">
          <div class="card-main">
            <strong>{{ a.name }}</strong>
            <span class="state" :data-state="a.state">{{ a.state }}</span>
            <a v-if="a.state === 'running'" :href="a.url" target="_blank">{{ a.url }}</a>
          </div>
          <button
            :disabled="uninstall.isPending.value"
            @click="uninstall.mutate(a.id)"
          >
            Uninstall
          </button>
        </li>
      </ul>
    </section>

    <section>
      <h2>Catalog</h2>
      <p v-if="catalog.isLoading.value" class="muted">Loading…</p>
      <ul v-else class="cards">
        <li v-for="c in catalog.data.value?.apps ?? []" :key="c.id" class="card">
          <div class="card-main">
            <strong>{{ c.name }}</strong>
            <span class="muted">v{{ c.version }}</span>
          </div>
          <button
            :disabled="install.isPending.value || installedManifest(c.id)"
            @click="install.mutate(c.id)"
          >
            {{ installedManifest(c.id) ? "Installed" : install.isPending.value ? "Installing…" : "Install" }}
          </button>
        </li>
      </ul>
      <p v-if="install.isError.value" class="error">{{ (install.error.value as Error).message }}</p>
    </section>

    <section>
      <h2>Add custom app</h2>
      <form class="custom" @submit.prevent="installCustom.mutate()">
        <div class="row">
          <input v-model="customName" placeholder="App name" required />
          <input v-model.number="customPort" type="number" placeholder="Main port" required />
        </div>
        <textarea
          v-model="customCompose"
          rows="8"
          spellcheck="false"
          placeholder="Paste a docker-compose.yml…"
          required
        />
        <div class="row">
          <button type="submit" :disabled="installCustom.isPending.value">
            {{ installCustom.isPending.value ? "Installing…" : "Install custom app" }}
          </button>
        </div>
        <p v-if="installCustom.isError.value" class="error">
          {{ (installCustom.error.value as ApiError).message }}
        </p>
      </form>
    </section>
  </main>
</template>

<style>
:root { font-family: ui-sans-serif, system-ui, sans-serif; color: #1a1a1a; }
body { margin: 0; background: #f6f6f7; }
main { max-width: 720px; margin: 0 auto; padding: 2rem 1rem; }
header { display: flex; align-items: center; gap: 0.75rem; margin-bottom: 1.5rem; }
.header-bell { margin-left: auto; }
h1 { margin: 0; font-size: 1.6rem; }
.tag { font-size: 0.75rem; color: #888; border: 1px solid #ddd; border-radius: 999px; padding: 2px 8px; }
h2 { font-size: 1rem; text-transform: uppercase; letter-spacing: 0.04em; color: #666; margin: 1.5rem 0 0.5rem; }
.cards { list-style: none; padding: 0; margin: 0; display: flex; flex-direction: column; gap: 0.5rem; }
.card { display: flex; align-items: center; justify-content: space-between; background: #fff; border: 1px solid #e6e6e8; border-radius: 10px; padding: 0.75rem 1rem; }
.card-main { display: flex; align-items: center; gap: 0.6rem; }
.state { font-size: 0.7rem; text-transform: uppercase; padding: 2px 6px; border-radius: 6px; background: #eee; }
.state[data-state="running"] { background: #e3f6e8; color: #1b6b34; }
.state[data-state="failed"] { background: #fde6e6; color: #a11; }
.state[data-state="installing"], .state[data-state="uninstalling"] { background: #fff3d6; color: #8a6d1b; }
a { color: #2b6cb0; font-size: 0.85rem; }
.muted { color: #999; font-size: 0.85rem; }
.error { color: #a11; font-size: 0.85rem; }
button { border: 1px solid #ccc; background: #fafafa; border-radius: 8px; padding: 0.4rem 0.9rem; cursor: pointer; font-size: 0.85rem; }
button:disabled { opacity: 0.5; cursor: default; }
button:not(:disabled):hover { background: #f0f0f0; }
.custom { display: flex; flex-direction: column; gap: 0.5rem; }
.custom .row { display: flex; gap: 0.5rem; }
.custom input { flex: 1; border: 1px solid #ddd; border-radius: 8px; padding: 0.45rem 0.7rem; font-size: 0.9rem; }
.custom input[type="number"] { flex: 0 0 8rem; }
.custom textarea { border: 1px solid #ddd; border-radius: 8px; padding: 0.6rem 0.8rem; font-family: ui-monospace, monospace; font-size: 0.8rem; resize: vertical; }
</style>
