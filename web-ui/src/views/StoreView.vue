<script setup lang="ts">
// Store — browse/install apps (DASHBOARD.md # global navigation). Catalog
// installs go through a consent + configuration dialog: clicking Install (or
// "Install for the whole household" from the split-button dropdown) fetches
// GET /api/v1/catalog/:id/install-plan (advisory), renders InstallDialog with
// the scope pre-set and per-folder source/subfolder elections, then submits to
// POST /api/v1/apps as config.folders[].
// Duplicate-install (409 duplicate-install) surfaces the existing-copy summary
// and offers "Install my own copy" which re-submits with confirm:true.
// 422 election-validation errors are passed into the dialog and displayed inline.
// Household instances show "Open shared app" + a secondary Install button;
// the caller's own personal instance shows "Open"; otherwise "Install".
import { ref, computed } from "vue";
import { useQuery, useMutation, useQueryClient } from "@tanstack/vue-query";
import { useAuth } from "../auth";
import {
  api,
  waitForJob,
  ApiError,
  type CatalogEntry,
  type Instance,
  type Job,
  type InstallPlan,
  type InstallRequest,
  type Scope,
} from "../api";
import InstallDialog from "../components/InstallDialog.vue";
import SplitButton from "../components/SplitButton.vue";

const qc = useQueryClient();
const { currentUser, singleUserMode } = useAuth();

const catalog = useQuery({
  queryKey: ["catalog"],
  queryFn: () => api.get<{ apps: CatalogEntry[] }>("/catalog"),
});

const apps = useQuery({
  queryKey: ["apps"],
  queryFn: () => api.get<{ apps: Instance[] }>("/apps"),
});

// ── Install-plan dialog state ─────────────────────────────────────────────────

// planFor is the catalog id whose install-plan we're fetching/showing.
const planFor = ref<string | null>(null);
// dialogScope is the scope the split-button selected before opening the dialog.
const dialogScope = ref<Scope>("personal");
// dialogError is passed into InstallDialog for 422 inline display.
const dialogError = ref<string | null>(null);
// duplicateInfo holds the ApiError.message from a 409 duplicate-install response.
const duplicateInfo = ref<string | null>(null);
// pendingRequest holds the last InstallRequest we sent (for confirm retry).
const pendingRequest = ref<InstallRequest | null>(null);
// installingId is the manifest id whose install job is running. Set once the
// POST is accepted (202) — drives the row's "Installing…"/disabled state after
// the dialog has closed, independent of which row's dialog was last open.
const installingId = ref<string | null>(null);
// installError surfaces a failure that happens *during* the job (after the
// dialog has already closed), as a dismissable banner.
const installError = ref<string | null>(null);

const installPlanQuery = useQuery({
  queryKey: computed(() => ["install-plan", planFor.value]),
  queryFn: () => api.get<InstallPlan>(`/catalog/${planFor.value}/install-plan`),
  enabled: computed(() => planFor.value !== null),
  staleTime: 0,
  // The plan for a given (id, role) is stable for the life of a dialog; a
  // background refetch would swap props.plan out from under an open dialog.
  refetchOnWindowFocus: false,
});

// activePlan is the plan to show, derived from the query — null while no dialog
// is requested or the fetch hasn't resolved. Deriving (not mirroring via a
// queryFn side-effect) keeps a background refetch from mutating dialog state.
const activePlan = computed<InstallPlan | null>(() =>
  planFor.value !== null ? (installPlanQuery.data.value ?? null) : null,
);

function openInstallDialog(catalogId: string, scope: Scope = "personal") {
  dialogError.value = null;
  duplicateInfo.value = null;
  pendingRequest.value = null;
  dialogScope.value = scope;
  planFor.value = catalogId;
}

function closeDialog() {
  planFor.value = null;
  dialogError.value = null;
  duplicateInfo.value = null;
  pendingRequest.value = null;
}

// ── Install mutation ──────────────────────────────────────────────────────────

const install = useMutation({
  mutationFn: async (req: InstallRequest) => {
    const job = await api.post<Job>("/apps", req);
    // POST accepted (202) → the job is running. 409 duplicate / 422 election
    // rejection would have thrown above, with the dialog still open. Now that
    // the install has started, close the dialog and mark the row installing.
    installingId.value = req.manifest_id;
    planFor.value = null;
    const done = await waitForJob(job.job_id);
    // waitForJob resolves for any terminal status; a failed/cancelled job is an
    // install failure, not a success — throw so onError surfaces it.
    if (done.status !== "completed") {
      throw new Error(done.error?.message ?? "The install didn't finish.");
    }
    return done;
  },
  onSuccess: () => {
    closeDialog();
  },
  onError: (err: unknown) => {
    if (err instanceof ApiError && err.code === "duplicate-install") {
      // Thrown at POST time, dialog still open → warn-don't-block banner.
      duplicateInfo.value = err.message;
    } else if (installingId.value) {
      // Failed during the job, after the dialog closed → standalone banner.
      installError.value = (err as Error).message;
    } else {
      // Failed at POST (422 election rejection) → inline in the open dialog.
      dialogError.value = (err as Error).message;
    }
  },
  onSettled: async () => {
    // Keep the row in "Installing…" until the apps list reflects the new
    // instance, so it flips straight to "Open" with no "Install" flicker.
    await qc.invalidateQueries({ queryKey: ["apps"] });
    installingId.value = null;
  },
});

function handleSubmit(req: InstallRequest) {
  dialogError.value = null;
  duplicateInfo.value = null;
  installError.value = null;
  pendingRequest.value = req;
  install.mutate(req);
}

function handleConfirmDuplicate() {
  if (!pendingRequest.value) return;
  const req = { ...pendingRequest.value, confirm: true };
  duplicateInfo.value = null;
  install.mutate(req);
}

// ── Per-row button logic ──────────────────────────────────────────────────────

// rowInstances maps manifest_id → the caller-relevant instances, computed once
// per apps/user change so each row's button is an O(1) lookup (not a filter).
const rowInstances = computed(() => {
  const uid = currentUser.value?.id;
  const m = new Map<string, { household?: Instance; ownPersonal?: Instance }>();
  for (const a of apps.data.value?.apps ?? []) {
    const e = m.get(a.manifest_id) ?? {};
    if (a.scope === "household") e.household ??= a;
    else if (a.scope === "personal" && a.owner_user_id === uid) e.ownPersonal ??= a;
    m.set(a.manifest_id, e);
  }
  return m;
});

function householdInstance(manifestId: string): Instance | undefined {
  return rowInstances.value.get(manifestId)?.household;
}

function ownPersonalInstance(manifestId: string): Instance | undefined {
  return rowInstances.value.get(manifestId)?.ownPersonal;
}

// householdDropdownItems is a computed map from manifest_id → dropdown items
// for the split-button. Recomputed only when role or singleUserMode changes,
// not on every render cycle. Empty array = plain button (no chevron).
const householdDropdownItems = computed(() => {
  const showDropdown = currentUser.value?.role === "admin" && !singleUserMode.value;
  return (catalogId: string) =>
    showDropdown
      ? [{ label: "Install for the whole household", action: () => openInstallDialog(catalogId, "household") }]
      : [];
});

// ── Custom app install ────────────────────────────────────────────────────────

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

          <div class="flex items-center gap-2">
            <!-- Household instance exists: "Open shared app" link + Install button -->
            <template v-if="householdInstance(c.id)">
              <a
                :href="householdInstance(c.id)!.url"
                target="_blank"
                rel="noopener"
                class="rounded-lg border border-border px-3 py-1.5 text-sm hover:bg-muted"
              >
                Open shared app
              </a>
              <SplitButton
                :label="installingId === c.id ? 'Installing…' : 'Install'"
                :disabled="installingId === c.id"
                :items="householdDropdownItems(c.id)"
                @click="openInstallDialog(c.id)"
              />
            </template>

            <!-- Caller's own personal instance exists: "Open" link only -->
            <template v-else-if="ownPersonalInstance(c.id)">
              <a
                :href="ownPersonalInstance(c.id)!.url"
                target="_blank"
                rel="noopener"
                class="rounded-lg border border-border px-3 py-1.5 text-sm hover:bg-muted"
              >
                Open
              </a>
            </template>

            <!-- No visible instance: Install button -->
            <template v-else>
              <SplitButton
                :label="installingId === c.id ? 'Installing…' : 'Install'"
                :disabled="installingId === c.id"
                :items="householdDropdownItems(c.id)"
                @click="openInstallDialog(c.id)"
              />
            </template>
          </div>
        </li>
      </ul>
    </section>

    <!-- Duplicate-install warning (409 duplicate-install) -->
    <div
      v-if="duplicateInfo"
      class="rounded-xl border border-border bg-card px-4 py-3 space-y-2"
    >
      <p class="text-sm">{{ duplicateInfo }}</p>
      <div class="flex gap-2">
        <button
          class="rounded-lg border border-border px-3 py-1.5 text-sm hover:bg-muted disabled:opacity-50"
          :disabled="install.isPending.value"
          @click="handleConfirmDuplicate"
        >
          {{ install.isPending.value ? "Installing…" : "Install my own copy" }}
        </button>
        <button
          class="rounded-lg border border-border px-3 py-1.5 text-sm hover:bg-muted"
          @click="closeDialog"
        >
          Cancel
        </button>
      </div>
    </div>

    <!-- Install failed after the dialog closed (job failure / host 5xx) -->
    <div
      v-if="installError"
      class="rounded-xl border border-destructive/40 bg-destructive/10 px-4 py-3 space-y-2"
    >
      <p class="text-sm text-destructive">{{ installError }}</p>
      <button
        class="rounded-lg border border-border px-3 py-1.5 text-sm hover:bg-muted"
        @click="installError = null"
      >
        Dismiss
      </button>
    </div>

    <!-- Install consent dialog -->
    <InstallDialog
      v-if="activePlan && !duplicateInfo"
      :plan="activePlan"
      :scope="dialogScope"
      :submit-error="dialogError"
      @submit="handleSubmit"
      @cancel="closeDialog"
    />

    <!-- Plan loading indicator (shown while dialog hasn't appeared yet) -->
    <p
      v-if="planFor && !activePlan && installPlanQuery.isFetching.value"
      class="text-sm text-muted-foreground"
    >
      Loading install plan…
    </p>
    <p
      v-if="installPlanQuery.isError.value && planFor"
      class="text-sm text-destructive"
    >
      {{ (installPlanQuery.error.value as Error).message }}
    </p>

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
