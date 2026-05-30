<script setup lang="ts">
// Store — browse/install apps (DASHBOARD.md # global navigation). This is the
// former dev Dashboard's catalog + custom-install surface, moved to its own
// route now that Home is the launcher. Install authorization is enforced by the
// brain (DASHBOARD.md # install authorization): admins default to a household
// instance, members are forced to a personal one. The explicit scope picker for
// admins and the warn-don't-block duplicate-confirm dialog are follow-ups; for
// now we install with the brain's default scope and hide the button once a
// caller-visible instance of that manifest exists (sidestepping the 409).
import { ref } from "vue";
import { useQuery, useMutation, useQueryClient } from "@tanstack/vue-query";
import { api, waitForJob, type ApiError, type CatalogEntry, type Instance, type Job } from "../api";

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
  <div class="space-y-8 pt-2">
    <section class="space-y-3">
      <h2 class="text-xs font-medium uppercase tracking-wide text-muted-foreground">Catalog</h2>
      <p v-if="catalog.isLoading.value" class="text-sm text-muted-foreground">Loading…</p>
      <ul v-else class="space-y-2">
        <li
          v-for="c in catalog.data.value?.apps ?? []"
          :key="c.id"
          class="flex items-center justify-between rounded-xl border border-border bg-card px-4 py-3"
        >
          <div class="flex items-baseline gap-2">
            <strong class="text-sm">{{ c.name }}</strong>
            <span class="text-xs text-muted-foreground">v{{ c.version }}</span>
          </div>
          <button
            class="rounded-lg border border-border px-3 py-1.5 text-sm hover:bg-muted disabled:opacity-50"
            :disabled="install.isPending.value || installedManifest(c.id)"
            @click="install.mutate(c.id)"
          >
            {{ installedManifest(c.id) ? "Installed" : install.isPending.value ? "Installing…" : "Install" }}
          </button>
        </li>
      </ul>
      <p v-if="install.isError.value" class="text-sm text-destructive">
        {{ (install.error.value as ApiError).message }}
      </p>
    </section>

    <section class="space-y-3">
      <h2 class="text-xs font-medium uppercase tracking-wide text-muted-foreground">Add custom app</h2>
      <form class="flex flex-col gap-2" @submit.prevent="installCustom.mutate()">
        <div class="flex gap-2">
          <input
            v-model="customName"
            placeholder="App name"
            required
            class="flex-1 rounded-lg border border-border px-3 py-2 text-sm"
          />
          <input
            v-model.number="customPort"
            type="number"
            placeholder="Main port"
            required
            class="w-32 rounded-lg border border-border px-3 py-2 text-sm"
          />
        </div>
        <textarea
          v-model="customCompose"
          rows="8"
          spellcheck="false"
          placeholder="Paste a docker-compose.yml…"
          required
          class="rounded-lg border border-border px-3 py-2 font-mono text-xs"
        />
        <div>
          <button
            type="submit"
            class="rounded-lg border border-border px-3 py-1.5 text-sm hover:bg-muted disabled:opacity-50"
            :disabled="installCustom.isPending.value"
          >
            {{ installCustom.isPending.value ? "Installing…" : "Install custom app" }}
          </button>
        </div>
        <p v-if="installCustom.isError.value" class="text-sm text-destructive">
          {{ (installCustom.error.value as ApiError).message }}
        </p>
      </form>
    </section>
  </div>
</template>
