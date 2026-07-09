<script setup lang="ts">
// Files — the in-dashboard file manager (FILES.md). A zero-setup browse surface
// over the two roots every user has: "My files" (their /home/<user>/) and
// "Shared" (/srv/malmo/shared/). Browse, download, upload, new folder, rename,
// move, copy, delete — all as the signed-in user (the brain forwards to
// host-agent, which acts as that user's UID). No cross-user browse, for any role.
import { ref, computed } from "vue";
import { useQuery, useMutation, useQueryClient } from "@tanstack/vue-query";
import {
  Folder,
  File as FileIcon,
  FolderPlus,
  Upload,
  Download,
  Pencil,
  FolderInput,
  Copy as CopyIcon,
  Trash2,
  ChevronRight,
  Eye,
  EyeOff,
  Loader2,
  Check,
  X,
} from "lucide-vue-next";
import { ApiError, type FileEntry } from "@/api";
import {
  listFiles,
  makeFolder,
  deleteEntry,
  moveEntry,
  copyEntry,
  downloadURL,
  uploadFile,
  joinPath,
  sortEntries,
  formatBytes,
  type FileRoot,
} from "@/useFiles";
import { pushErrorToast, pushSuccessToast } from "@/toasts";
import Heading from "@/components/ui/Heading.vue";
import Button from "@/components/ui/Button.vue";
import FileDestinationDialog from "@/components/FileDestinationDialog.vue";

const qc = useQueryClient();

const root = ref<FileRoot>("home");
const path = ref<string>("");
const showHidden = ref(false);

const listing = useQuery({
  queryKey: computed(() => ["files", root.value, path.value]),
  queryFn: () => listFiles(root.value, path.value),
});

const entries = computed(() => {
  const all = sortEntries(listing.data.value?.entries ?? []);
  return showHidden.value ? all : all.filter((e) => !e.hidden);
});
const crumbs = computed(() => (path.value ? path.value.split("/") : []));

function invalidate() {
  return qc.invalidateQueries({ queryKey: ["files"] });
}
function toastError(e: unknown) {
  pushErrorToast(e instanceof ApiError ? e.message : "Something went wrong");
}

// --- navigation ---
function switchRoot(r: FileRoot) {
  root.value = r;
  path.value = "";
}
function openEntry(e: FileEntry) {
  if (e.dir) path.value = joinPath(path.value, e.name);
  else download(e);
}
function download(e: FileEntry) {
  const a = document.createElement("a");
  a.href = downloadURL(root.value, joinPath(path.value, e.name));
  a.download = e.name;
  document.body.appendChild(a);
  a.click();
  a.remove();
}

// --- new folder ---
const newFolderName = ref<string | null>(null);
const mkdirMut = useMutation({
  mutationFn: (name: string) => makeFolder(root.value, joinPath(path.value, name)),
  onSuccess: () => {
    newFolderName.value = null;
    void invalidate();
  },
  onError: toastError,
});
function submitNewFolder() {
  const name = (newFolderName.value ?? "").trim();
  if (name) mkdirMut.mutate(name);
}

// --- rename (a move within the same folder) ---
const renaming = ref<string | null>(null); // the entry name being renamed
const renameValue = ref("");
const renameMut = useMutation({
  mutationFn: (vars: { from: string; to: string }) =>
    moveEntry(
      { root: root.value, path: joinPath(path.value, vars.from) },
      { root: root.value, path: joinPath(path.value, vars.to) },
    ),
  onSuccess: () => {
    renaming.value = null;
    void invalidate();
  },
  onError: toastError,
});
function startRename(e: FileEntry) {
  renaming.value = e.name;
  renameValue.value = e.name;
}
function submitRename() {
  const from = renaming.value;
  const to = renameValue.value.trim();
  if (from && to && to !== from) renameMut.mutate({ from, to });
  else renaming.value = null;
}

// --- delete ---
const confirmDelete = ref<string | null>(null);
const deleteMut = useMutation({
  mutationFn: (name: string) => deleteEntry(root.value, joinPath(path.value, name)),
  onSuccess: () => {
    confirmDelete.value = null;
    void invalidate();
  },
  onError: toastError,
});

// --- move / copy ---
const transfer = ref<{ op: "move" | "copy"; entry: FileEntry } | null>(null);
async function onTransferPicked(destRoot: FileRoot, destPath: string) {
  const t = transfer.value;
  if (!t) return;
  const from = { root: root.value, path: joinPath(path.value, t.entry.name) };
  const to = { root: destRoot, path: destPath };
  try {
    if (t.op === "move") await moveEntry(from, to);
    else await copyEntry(from, to);
    pushSuccessToast(t.op === "move" ? "Moved" : "Copied");
    transfer.value = null;
    void invalidate();
  } catch (e) {
    toastError(e);
  }
}

// --- upload ---
const fileInput = ref<HTMLInputElement | null>(null);
const uploading = ref<{ name: string; pct: number } | null>(null);
async function onFilesPicked(ev: Event) {
  const input = ev.target as HTMLInputElement;
  const files = Array.from(input.files ?? []);
  input.value = ""; // allow re-picking the same file
  for (const file of files) {
    uploading.value = { name: file.name, pct: 0 };
    try {
      await uploadFile(root.value, joinPath(path.value, file.name), file, (pct) => {
        uploading.value = { name: file.name, pct };
      });
    } catch (e) {
      toastError(e);
    }
  }
  uploading.value = null;
  void invalidate();
}
</script>

<template>
  <section class="mx-auto w-full max-w-4xl">
    <Heading :level="2">Files</Heading>

    <!-- Root switch -->
    <div class="mt-4 flex gap-2">
      <button
        type="button"
        :class="[
          'rounded-full px-4 py-1.5 text-sm',
          root === 'home' ? 'bg-accent text-accent-foreground' : 'border border-border text-foreground hover:bg-muted',
        ]"
        @click="switchRoot('home')"
      >
        My files
      </button>
      <button
        type="button"
        :class="[
          'rounded-full px-4 py-1.5 text-sm',
          root === 'shared' ? 'bg-accent text-accent-foreground' : 'border border-border text-foreground hover:bg-muted',
        ]"
        @click="switchRoot('shared')"
      >
        Shared
      </button>
    </div>

    <!-- Toolbar: breadcrumb + actions -->
    <div class="mt-4 flex flex-wrap items-center justify-between gap-3">
      <div class="flex flex-wrap items-center gap-1 text-sm text-muted-foreground">
        <button type="button" class="hover:text-foreground" @click="path = ''">
          {{ root === "home" ? "My files" : "Shared" }}
        </button>
        <template v-for="(seg, i) in crumbs" :key="i">
          <ChevronRight class="size-3.5" />
          <button type="button" class="hover:text-foreground" @click="path = crumbs.slice(0, i + 1).join('/')">
            {{ seg }}
          </button>
        </template>
      </div>
      <div class="flex items-center gap-2">
        <button
          type="button"
          class="inline-flex items-center gap-1 rounded-lg border border-border px-3 py-1.5 text-sm hover:bg-muted"
          @click="showHidden = !showHidden"
        >
          <component :is="showHidden ? EyeOff : Eye" class="size-4" />
          {{ showHidden ? "Hide hidden" : "Show hidden" }}
        </button>
        <button
          type="button"
          class="inline-flex items-center gap-1 rounded-lg border border-border px-3 py-1.5 text-sm hover:bg-muted"
          @click="newFolderName = ''"
        >
          <FolderPlus class="size-4" /> New folder
        </button>
        <Button size="sm" @click="fileInput?.click()">
          <Upload class="size-4" /> Upload
        </Button>
        <input ref="fileInput" type="file" multiple class="hidden" @change="onFilesPicked" />
      </div>
    </div>

    <!-- Upload progress -->
    <div v-if="uploading" class="mt-3 flex items-center gap-2 text-sm text-muted-foreground">
      <Loader2 class="size-4 animate-spin" />
      Uploading {{ uploading.name }} — {{ uploading.pct }}%
    </div>

    <!-- New folder inline input -->
    <form v-if="newFolderName !== null" class="mt-3 flex items-center gap-2" @submit.prevent="submitNewFolder">
      <Folder class="size-4 text-accent" />
      <input
        v-model="newFolderName"
        autofocus
        placeholder="Folder name"
        class="flex-1 rounded-lg border border-border bg-background px-3 py-1.5 text-sm outline-none focus:border-accent"
      />
      <Button size="sm" type="submit" :disabled="mkdirMut.isPending.value">Create</Button>
      <Button size="sm" variant="secondary" type="button" @click="newFolderName = null">Cancel</Button>
    </form>

    <!-- Listing -->
    <div class="mt-4 rounded-xl border border-border bg-card">
      <p v-if="listing.isLoading.value" class="px-4 py-6 text-center text-sm text-muted-foreground">Loading…</p>
      <p v-else-if="listing.isError.value" class="px-4 py-6 text-center text-sm text-destructive">
        {{ listing.error.value instanceof ApiError ? listing.error.value.message : "Could not load this folder." }}
      </p>
      <p v-else-if="!entries.length" class="px-4 py-10 text-center text-sm text-muted-foreground">
        This folder is empty.
      </p>
      <ul v-else class="divide-y divide-border">
        <li v-for="e in entries" :key="e.name" class="flex flex-wrap items-center gap-2 px-4 py-2.5">
          <!-- Rename row -->
          <template v-if="renaming === e.name">
            <component :is="e.dir ? Folder : FileIcon" class="size-4 text-muted-foreground" />
            <input
              v-model="renameValue"
              class="flex-1 rounded-lg border border-border bg-background px-2 py-1 text-sm outline-none focus:border-accent"
              @keyup.enter="submitRename"
              @keyup.esc="renaming = null"
            />
            <button type="button" class="rounded-md p-1 text-accent hover:bg-muted" title="Save" @click="submitRename">
              <Check class="size-4" />
            </button>
            <button type="button" class="rounded-md p-1 text-muted-foreground hover:bg-muted" title="Cancel" @click="renaming = null">
              <X class="size-4" />
            </button>
          </template>

          <!-- Delete confirm row -->
          <template v-else-if="confirmDelete === e.name">
            <span class="flex-1 text-sm text-foreground">
              Delete <strong>{{ e.name }}</strong>? This can't be undone.
            </span>
            <Button size="sm" variant="secondary" type="button" @click="confirmDelete = null">Cancel</Button>
            <button
              type="button"
              class="rounded-lg bg-destructive px-3 py-1 text-sm text-destructive-foreground hover:opacity-90"
              :disabled="deleteMut.isPending.value"
              @click="deleteMut.mutate(e.name)"
            >
              Delete
            </button>
          </template>

          <!-- Normal row -->
          <template v-else>
            <button
              type="button"
              class="flex min-w-0 flex-1 items-center gap-2 text-left"
              @click="openEntry(e)"
            >
              <component :is="e.dir ? Folder : FileIcon" class="size-4 shrink-0" :class="e.dir ? 'text-accent' : 'text-muted-foreground'" />
              <span class="truncate text-sm text-foreground" :class="{ 'opacity-60': e.hidden }">{{ e.name }}</span>
            </button>
            <span class="w-20 text-right text-xs tabular-nums text-muted-foreground">
              {{ e.dir ? "—" : formatBytes(e.size_bytes) }}
            </span>
            <div class="flex items-center gap-0.5">
              <button v-if="!e.dir" type="button" class="rounded-md p-1.5 text-muted-foreground hover:bg-muted hover:text-foreground" title="Download" @click="download(e)">
                <Download class="size-4" />
              </button>
              <button type="button" class="rounded-md p-1.5 text-muted-foreground hover:bg-muted hover:text-foreground" title="Rename" @click="startRename(e)">
                <Pencil class="size-4" />
              </button>
              <button type="button" class="rounded-md p-1.5 text-muted-foreground hover:bg-muted hover:text-foreground" title="Move" @click="transfer = { op: 'move', entry: e }">
                <FolderInput class="size-4" />
              </button>
              <button type="button" class="rounded-md p-1.5 text-muted-foreground hover:bg-muted hover:text-foreground" title="Copy" @click="transfer = { op: 'copy', entry: e }">
                <CopyIcon class="size-4" />
              </button>
              <button type="button" class="rounded-md p-1.5 text-muted-foreground hover:bg-muted hover:text-destructive" title="Delete" @click="confirmDelete = e.name">
                <Trash2 class="size-4" />
              </button>
            </div>
          </template>
        </li>
      </ul>
    </div>

    <FileDestinationDialog
      v-if="transfer"
      :operation="transfer.op"
      :source-root="root"
      :source-path="joinPath(path, transfer.entry.name)"
      :source-name="transfer.entry.name"
      :source-is-dir="transfer.entry.dir"
      @close="transfer = null"
      @picked="onTransferPicked"
    />
  </section>
</template>
