<script setup lang="ts">
// Files — a top-level destination because "files are first-class" (DASHBOARD.md
// # global navigation). The in-dashboard file browser over the user's use-case
// folders and ~/Shared/ is specced in FILES.md; none of it is built yet — the
// brain has no /api/v1/files/* handlers and host-agent has no /v1/files/* ops —
// so this is a deliberate stub until that slice lands.
import { FolderOpen } from "lucide-vue-next";
</script>

<template>
  <div class="mt-16 flex flex-col items-center gap-3 text-center text-muted-foreground">
    <FolderOpen class="size-8" />
    <p>Files is coming soon.</p>
    <p class="max-w-sm text-xs">
      Your Photos, Documents, and shared folders will live here once the file
      browser ships.
    </p>
  </div>
</template>
