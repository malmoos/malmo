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
import { ref, computed, watch } from "vue";
import type { InstallPlan, InstallRequest, FolderElection, Scope } from "../api";

const props = defineProps<{
  plan: InstallPlan;
  submitError?: string | null;
}>();

const emit = defineEmits<{
  submit: [req: InstallRequest];
  cancel: [];
}>();

// ── Scope election ────────────────────────────────────────────────────────────

const electedScope = ref<Scope>(props.plan.scope_default);

const hasMultipleScopes = computed(() => props.plan.scope_options.length > 1);

// ── Per-folder elections ──────────────────────────────────────────────────────

// folderSources: reactive map of folder name → elected source string
const folderSources = ref<Record<string, string>>({});
// folderSubfolders: reactive map of folder name → subfolder string
const folderSubfolders = ref<Record<string, string>>({});

function initFolderDefaults(scope: Scope) {
  const sources: Record<string, string> = {};
  const subfolders: Record<string, string> = {};
  for (const f of props.plan.permissions.folders) {
    const menu = f.sources[scope];
    sources[f.folder] = menu.default;
    if (f.scope === "pick-subfolder") {
      // keep existing user entry if already typed, otherwise take the plan default
      subfolders[f.folder] = folderSubfolders.value[f.folder] ?? (f.subfolder_default ?? "");
    }
  }
  folderSources.value = sources;
  // only overwrite subfolders on first init (don't clobber user input on scope flip)
  if (Object.keys(folderSubfolders.value).length === 0) {
    folderSubfolders.value = subfolders;
  }
}

// Initialise on mount
initFolderDefaults(electedScope.value);

// Re-derive source defaults when scope flips (subfolder user input is preserved)
watch(electedScope, (newScope) => {
  const sources: Record<string, string> = {};
  for (const f of props.plan.permissions.folders) {
    const menu = f.sources[newScope];
    sources[f.folder] = menu.default;
  }
  folderSources.value = sources;
});

// ── Human-readable helpers ────────────────────────────────────────────────────

function capitalize(s: string): string {
  return s.charAt(0).toUpperCase() + s.slice(1);
}

function folderDisplayName(folder: string): string {
  return capitalize(folder);
}

function sourceLabel(folder: string, source: string): string {
  const name = folderDisplayName(folder);
  if (source === "shared") return `The household's shared ${name}`;
  return `Your ${name}`;
}

function scopeLabel(scope: Scope): string {
  return scope === "household" ? "For the whole household" : "Just for me";
}

// ── Submit ────────────────────────────────────────────────────────────────────

function handleSubmit() {
  const folderElections: FolderElection[] = props.plan.permissions.folders.map((f) => {
    const election: FolderElection = { folder: f.folder };
    election.source = folderSources.value[f.folder];
    if (f.scope === "pick-subfolder") {
      const sub = folderSubfolders.value[f.folder];
      if (sub) election.subfolder = sub;
    }
    return election;
  });

  const req: InstallRequest = {
    manifest_id: props.plan.manifest_id,
    scope: electedScope.value,
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

        <!-- Scope picker (admins only, when more than one option) -->
        <div v-if="hasMultipleScopes" class="space-y-1.5">
          <p class="text-xs font-medium uppercase tracking-wide text-muted-foreground">Install for</p>
          <div class="flex flex-col gap-1">
            <label
              v-for="opt in plan.scope_options"
              :key="opt"
              class="flex cursor-pointer items-center gap-2 rounded-lg border border-border px-3 py-2 text-sm hover:bg-muted"
              :class="electedScope === opt ? 'border-accent bg-muted' : ''"
            >
              <input
                type="radio"
                :value="opt"
                v-model="electedScope"
                class="accent-accent"
              />
              {{ scopeLabel(opt) }}
            </label>
          </div>
        </div>

        <!-- Fixed scope display (members — no picker) -->
        <div v-else class="text-sm text-muted-foreground">
          Installing as a personal app.
        </div>

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
              v-for="device in plan.permissions.devices"
              :key="device"
              class="flex items-start gap-2 text-sm"
            >
              <span class="mt-0.5 text-muted-foreground">•</span>
              Access device {{ device }}
            </li>
            <!-- Folder permissions -->
            <li
              v-for="f in plan.permissions.folders"
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
              v-if="!plan.permissions.internet && !plan.permissions.lan && !plan.permissions.gpu && plan.permissions.devices.length === 0 && plan.permissions.folders.length === 0"
              class="text-sm text-muted-foreground"
            >
              No special permissions required.
            </li>
          </ul>
        </div>

        <!-- Per-folder source pickers -->
        <div v-if="plan.permissions.folders.length > 0" class="space-y-3">
          <p class="text-xs font-medium uppercase tracking-wide text-muted-foreground">Folder sources</p>
          <div
            v-for="f in plan.permissions.folders"
            :key="f.folder"
            class="space-y-1.5 rounded-xl border border-border px-3 py-2.5"
          >
            <p class="text-sm font-medium">{{ folderDisplayName(f.folder) }}</p>

            <!-- Single option: fixed/disabled display -->
            <p
              v-if="f.sources[electedScope].options.length === 1"
              class="text-sm text-muted-foreground"
            >
              {{ sourceLabel(f.folder, f.sources[electedScope].options[0] ?? "") }}
            </p>

            <!-- Multiple options: radio picker -->
            <div v-else class="flex flex-col gap-1">
              <label
                v-for="opt in f.sources[electedScope].options"
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
