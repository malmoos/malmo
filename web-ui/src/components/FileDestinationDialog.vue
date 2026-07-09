<script setup lang="ts">
// Destination picker for move/copy (FILES.md v1 op set). Navigates the two roots
// and their folders, then confirms the target. A move/copy may cross roots
// (My files → Shared); the brain performs it as the user's UID, so it only
// succeeds where the user can write both ends.
import { ref, computed } from "vue";
import { useQuery } from "@tanstack/vue-query";
import { Folder, ChevronRight, CornerLeftUp } from "lucide-vue-next";
import Button from "@/components/ui/Button.vue";
import { listFiles, sortEntries, joinPath, parentPath, type FileRoot } from "@/useFiles";

const props = defineProps<{
  operation: "move" | "copy";
  sourceRoot: FileRoot;
  sourcePath: string;
  sourceName: string;
  sourceIsDir: boolean;
}>();
const emit = defineEmits<{
  close: [];
  picked: [root: FileRoot, path: string];
}>();

const navRoot = ref<FileRoot>(props.sourceRoot);
const navPath = ref<string>(parentPath(props.sourcePath)); // open in the source's own folder

const listing = useQuery({
  queryKey: computed(() => ["files", navRoot.value, navPath.value]),
  queryFn: () => listFiles(navRoot.value, navPath.value),
});
const folders = computed(() =>
  sortEntries(listing.data.value?.entries ?? []).filter((e) => e.dir && !e.hidden),
);
const crumbs = computed(() => (navPath.value ? navPath.value.split("/") : []));

function openFolder(name: string) {
  navPath.value = joinPath(navPath.value, name);
}
function switchRoot(r: FileRoot) {
  navRoot.value = r;
  navPath.value = "";
}

// The destination folder must differ from the source's own folder (same-root
// same-parent would clobber or no-op), and a folder can't be moved/copied into
// itself or a descendant.
const disabled = computed(() => {
  const sameRoot = navRoot.value === props.sourceRoot;
  if (sameRoot && navPath.value === parentPath(props.sourcePath)) return true;
  if (sameRoot && props.sourceIsDir) {
    if (navPath.value === props.sourcePath || navPath.value.startsWith(props.sourcePath + "/")) return true;
  }
  return false;
});

function confirm() {
  emit("picked", navRoot.value, joinPath(navPath.value, props.sourceName));
}
</script>

<template>
  <div class="fixed inset-0 z-50 grid place-items-center bg-black/40 px-4" @click.self="emit('close')">
    <div class="flex max-h-[80vh] w-full max-w-md flex-col rounded-xl border border-border bg-card p-6 shadow-lg">
      <h2 class="font-display text-lg text-foreground">
        {{ operation === "move" ? "Move" : "Copy" }} “{{ sourceName }}”
      </h2>
      <p class="mt-1 text-sm text-muted-foreground">Choose a destination folder.</p>

      <!-- Root switch -->
      <div class="mt-4 flex gap-2">
        <button
          type="button"
          :class="[
            'rounded-full px-3 py-1 text-sm',
            navRoot === 'home' ? 'bg-accent text-accent-foreground' : 'border border-border text-foreground hover:bg-muted',
          ]"
          @click="switchRoot('home')"
        >
          My files
        </button>
        <button
          type="button"
          :class="[
            'rounded-full px-3 py-1 text-sm',
            navRoot === 'shared' ? 'bg-accent text-accent-foreground' : 'border border-border text-foreground hover:bg-muted',
          ]"
          @click="switchRoot('shared')"
        >
          Shared
        </button>
      </div>

      <!-- Breadcrumb -->
      <div class="mt-3 flex flex-wrap items-center gap-1 text-sm text-muted-foreground">
        <button type="button" class="hover:text-foreground" @click="navPath = ''">
          {{ navRoot === "home" ? "My files" : "Shared" }}
        </button>
        <template v-for="(seg, i) in crumbs" :key="i">
          <ChevronRight class="size-3" />
          <button type="button" class="hover:text-foreground" @click="navPath = crumbs.slice(0, i + 1).join('/')">
            {{ seg }}
          </button>
        </template>
      </div>

      <!-- Folder list -->
      <ul class="mt-3 min-h-24 flex-1 overflow-y-auto rounded-lg border border-border">
        <li v-if="navPath">
          <button
            type="button"
            class="flex w-full items-center gap-2 px-3 py-2 text-sm text-muted-foreground hover:bg-muted"
            @click="navPath = parentPath(navPath)"
          >
            <CornerLeftUp class="size-4" /> ..
          </button>
        </li>
        <li v-for="f in folders" :key="f.name">
          <button
            type="button"
            class="flex w-full items-center gap-2 px-3 py-2 text-sm text-foreground hover:bg-muted"
            @click="openFolder(f.name)"
          >
            <Folder class="size-4 text-accent" /> {{ f.name }}
          </button>
        </li>
        <li v-if="!folders.length && !navPath" class="px-3 py-2 text-sm text-muted-foreground">
          No subfolders here.
        </li>
      </ul>

      <div class="mt-5 flex justify-end gap-2">
        <Button variant="secondary" size="sm" @click="emit('close')">Cancel</Button>
        <Button size="sm" :disabled="disabled" @click="confirm">
          {{ operation === "move" ? "Move here" : "Copy here" }}
        </Button>
      </div>
    </div>
  </div>
</template>
