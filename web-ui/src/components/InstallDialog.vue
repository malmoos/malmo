<script setup lang="ts">
// InstallDialog — consent + configuration modal for catalog app installs
// (DASHBOARD.md # install authorization, # warn-don't-block).
// Driven by a GET /api/v1/catalog/:id/install-plan response (advisory).
// The parent owns the mutation; this component owns local election state
// and emits the fully-assembled InstallRequest on "submit".
//
// UI owns ALL wording — the brain returns structured enums; we write sentences.
// Write-mode folder warnings are visually distinct (warning/red) per spec
// (APP_MANIFEST.md:218, APP_ISOLATION.md # User content).
import { computed, ref } from "vue";
import type { InstallPlan, InstallPlanFolder, InstallRequest, FolderElection, Scope } from "../api";
import { useAuth } from "../auth";
import { formatSize } from "../utils";

const props = defineProps<{
  plan: InstallPlan;
  scope: Scope;
  submitError?: string | null;
}>();

const { singleUserMode } = useAuth();

const emit = defineEmits<{
  submit: [req: InstallRequest];
  cancel: [];
}>();

// folders / devices normalize the brain's nullable permission arrays (a nil Go
// slice serializes to null in the OpenAPI schema) so the template and the
// election builder can iterate them without per-site guards.
const folders = computed(() => props.plan.permissions.folders ?? []);
const devices = computed(() => props.plan.permissions.devices ?? []);

// ── Storage footprint (DASHBOARD.md # the consent screen shows the on-disk
// footprint) ──────────────────────────────────────────────────────────────────
// The install-plan footprint is box-specific: download_bytes already subtracts
// images this box has cached. The brain returns raw bytes; all wording + unit
// rounding live here. The block is skipped entirely for an unsized manifest
// (whoami-style: no images sized, no estimate) rather than showing a bare 0.
const fp = computed(() => props.plan.footprint);
const hasFootprint = computed(
  () => fp.value.image_disk_bytes > 0 || fp.value.estimated_state_bytes != null || fp.value.download_bytes > 0,
);
const downloadLine = computed(() =>
  fp.value.download_bytes > 0
    ? `Download about ${formatSize(fp.value.download_bytes)}.`
    : "Already downloaded — nothing new to fetch.",
);
// The space line leads with the immediate on-disk image size; working data is a
// qualitative "grows as you use it" (its concrete estimate feeds only the
// not-enough-space math). For an estimate-only manifest (no sized image) we drop
// to the qualitative phrasing.
const usesLine = computed(() => {
  const grows = fp.value.estimated_state_bytes != null;
  if (fp.value.image_disk_bytes > 0) {
    return `Uses about ${formatSize(fp.value.image_disk_bytes)} on your box${grows ? ", and grows as you use it" : ""}.`;
  }
  return grows ? "Uses space on your box that grows as you use it." : "Uses some space on your box.";
});
// Warn when the full projected need (image + its own working data) approaches the
// live free space — surfaced, never a hard block (DASHBOARD.md). 90% is a UI
// judgement of "approaches"; free_bytes == 0 means the brain couldn't measure, so
// we stay silent rather than cry wolf.
const projectedBytes = computed(() => fp.value.image_disk_bytes + (fp.value.estimated_state_bytes ?? 0));
const notEnoughSpace = computed(() => fp.value.free_bytes > 0 && projectedBytes.value >= fp.value.free_bytes * 0.9);

// sourceOptions is a folder's (also-nullable) source menu at the current scope —
// the household/personal pick selects which menu applies.
function sourceOptions(f: InstallPlanFolder): string[] {
  return f.sources[props.scope].options ?? [];
}

// ── Per-folder elections ──────────────────────────────────────────────────────

// folderSources: reactive map of folder name → elected source string
const folderSources = ref<Record<string, string>>({});
// folderSubfolders: reactive map of folder name → subfolder string
const folderSubfolders = ref<Record<string, string>>({});

function initFolderDefaults(scope: Scope) {
  const sources: Record<string, string> = {};
  const subfolders: Record<string, string> = {};
  for (const f of folders.value) {
    const menu = f.sources[scope];
    sources[f.folder] = menu.default;
    if (f.scope === "pick-subfolder") {
      subfolders[f.folder] = folderSubfolders.value[f.folder] ?? (f.subfolder_default ?? "");
    }
  }
  folderSources.value = sources;
  if (Object.keys(folderSubfolders.value).length === 0) {
    folderSubfolders.value = subfolders;
  }
}

initFolderDefaults(props.scope);

// ── Human-readable helpers ────────────────────────────────────────────────────

function capitalize(s: string): string {
  return s.charAt(0).toUpperCase() + s.slice(1);
}

function folderDisplayName(folder: string): string {
  return capitalize(folder);
}

function sourceLabel(folder: string, source: string): string {
  const name = folderDisplayName(folder);
  if (source === "shared") {
    return singleUserMode.value
      ? `Shared ${name} (accessible from your other devices)`
      : `The household's shared ${name}`;
  }
  return `Your ${name}`;
}

// ── Submit ────────────────────────────────────────────────────────────────────

function handleSubmit() {
  const folderElections: FolderElection[] = folders.value.map((f) => {
    const election: FolderElection = { folder: f.folder };
    if (sourceOptions(f).length > 1) {
      election.source = folderSources.value[f.folder];
    }
    if (f.scope === "pick-subfolder") {
      const sub = folderSubfolders.value[f.folder];
      if (sub) election.subfolder = sub;
    }
    return election;
  });

  const req: InstallRequest = {
    manifest_id: props.plan.manifest_id,
    scope: props.scope,
    config: { folders: folderElections },
  };

  emit("submit", req);
}
</script>

<template>
  <!-- Backdrop -->
  <div
    class="fixed inset-0 z-30 flex items-center justify-center bg-black/40 px-4"
    @click.self="emit('cancel')"
    @keydown.escape.window="emit('cancel')"
  >
    <div class="w-full max-w-md rounded-2xl border border-border bg-card shadow-xl">

      <!-- Header -->
      <div class="border-b border-border px-5 py-4">
        <div class="flex items-baseline gap-2">
          <h2 class="text-base font-semibold">{{ plan.name }}</h2>
          <span class="text-xs text-muted-foreground">v{{ plan.version }}</span>
        </div>
      </div>

      <div class="space-y-5 px-5 py-4">

        <!-- Permissions section -->
        <div class="space-y-1.5">
          <p class="text-xs font-medium uppercase tracking-wide text-muted-foreground">Permissions</p>
          <ul class="space-y-1">
            <li v-if="plan.permissions.internet" class="flex items-start gap-2 text-sm">
              <span class="mt-0.5 text-muted-foreground">•</span>
              Connect to the internet
            </li>
            <li v-if="plan.permissions.lan" class="flex items-start gap-2 text-sm">
              <span class="mt-0.5 text-muted-foreground">•</span>
              Reach other devices on your network
            </li>
            <li v-if="plan.permissions.gpu" class="flex items-start gap-2 text-sm">
              <span class="mt-0.5 text-muted-foreground">•</span>
              Use the graphics card
            </li>
            <li
              v-for="device in devices"
              :key="device"
              class="flex items-start gap-2 text-sm"
            >
              <span class="mt-0.5 text-muted-foreground">•</span>
              Access device {{ device }}
            </li>
            <!-- Folder permissions -->
            <li
              v-for="f in folders"
              :key="f.folder"
              class="flex items-start gap-2 text-sm"
              :class="f.mode === 'write' ? 'font-medium text-destructive' : ''"
            >
              <span class="mt-0.5" :class="f.mode === 'write' ? 'text-destructive' : 'text-muted-foreground'">•</span>
              <span v-if="f.mode === 'write'">
                Add, change, and delete files in your {{ folderDisplayName(f.folder) }} folder
              </span>
              <span v-else>
                Read files in your {{ folderDisplayName(f.folder) }} folder
              </span>
            </li>
            <li
              v-if="!plan.permissions.internet && !plan.permissions.lan && !plan.permissions.gpu && devices.length === 0 && folders.length === 0"
              class="text-sm text-muted-foreground"
            >
              No special permissions required.
            </li>
          </ul>
        </div>

        <!-- Per-folder source pickers -->
        <div v-if="folders.length > 0" class="space-y-3">
          <p class="text-xs font-medium uppercase tracking-wide text-muted-foreground">Folder sources</p>
          <div
            v-for="f in folders"
            :key="f.folder"
            class="space-y-1.5 rounded-xl border border-border px-3 py-2.5"
          >
            <p class="text-sm font-medium">{{ folderDisplayName(f.folder) }}</p>

            <!-- Single option: fixed/disabled display -->
            <p
              v-if="sourceOptions(f).length === 1"
              class="text-sm text-muted-foreground"
            >
              {{ sourceLabel(f.folder, sourceOptions(f)[0] ?? "") }}
            </p>

            <!-- Multiple options: radio picker -->
            <div v-else class="flex flex-col gap-1">
              <label
                v-for="opt in sourceOptions(f)"
                :key="opt"
                class="flex cursor-pointer items-center gap-2 rounded-lg border border-border px-2.5 py-1.5 text-sm hover:bg-muted"
                :class="folderSources[f.folder] === opt ? 'border-accent bg-muted' : ''"
              >
                <input
                  type="radio"
                  :name="`folder-source-${f.folder}`"
                  :value="opt"
                  v-model="folderSources[f.folder]"
                  class="accent-accent"
                />
                {{ sourceLabel(f.folder, opt) }}
              </label>
            </div>

            <!-- Subfolder input (pick-subfolder only) -->
            <div v-if="f.scope === 'pick-subfolder'" class="pt-1">
              <label class="block text-xs text-muted-foreground mb-1">
                Which subfolder should this app manage?
              </label>
              <input
                v-model="folderSubfolders[f.folder]"
                type="text"
                class="w-full rounded-lg border border-border px-3 py-1.5 text-sm"
                :placeholder="f.subfolder_default ?? ''"
              />
            </div>
          </div>
        </div>

        <!-- Storage footprint — what installing costs the box (DASHBOARD.md #
             the consent screen shows the on-disk footprint). -->
        <div v-if="hasFootprint" class="space-y-1.5">
          <p class="text-xs font-medium uppercase tracking-wide text-muted-foreground">Storage</p>
          <ul class="space-y-1 text-sm">
            <li v-if="fp.image_disk_bytes > 0 || fp.download_bytes > 0" class="flex items-start gap-2">
              <span class="mt-0.5 text-muted-foreground">•</span>{{ downloadLine }}
            </li>
            <li class="flex items-start gap-2">
              <span class="mt-0.5 text-muted-foreground">•</span>{{ usesLine }}
            </li>
            <li class="flex items-start gap-2 text-muted-foreground">
              <span class="mt-0.5">•</span>Your own files stay in your folders, not inside the app.
            </li>
          </ul>
          <!-- Not-enough-space: surfaced, not blocking (the Install button stays
               enabled). -->
          <p
            v-if="notEnoughSpace"
            class="rounded-lg bg-destructive/10 px-3 py-2 text-sm text-destructive"
          >
            This might not fit — only about {{ formatSize(fp.free_bytes) }} free on your box. You can still install.
          </p>
        </div>

        <!-- 422 inline error -->
        <p v-if="submitError" class="rounded-lg bg-destructive/10 px-3 py-2 text-sm text-destructive">
          {{ submitError }}
        </p>

      </div>

      <!-- Footer -->
      <div class="flex justify-end gap-2 border-t border-border px-5 py-3">
        <button
          class="rounded-lg border border-border px-3 py-1.5 text-sm hover:bg-muted"
          @click="emit('cancel')"
        >
          Cancel
        </button>
        <button
          class="rounded-lg bg-accent px-4 py-1.5 text-sm font-medium text-accent-foreground hover:opacity-90 disabled:opacity-50"
          @click="handleSubmit"
        >
          Install
        </button>
      </div>

    </div>
  </div>
</template>
