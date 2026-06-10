<script setup lang="ts">
// Settings → About — what this box is. Deliberately minimal: there is no
// version/box-name endpoint exposed to the UI yet, so this renders only what's
// genuinely known (product identity + project link) rather than fabricating
// system facts. When the brain exposes a build/version + box-name surface, this
// section grows to show it.
</script>

<template>
  <section class="space-y-3">
    <h2 class="text-xs font-medium uppercase tracking-wide text-muted-foreground">About</h2>
    <div class="space-y-2 rounded-xl border border-border bg-card px-4 py-4">
      <div class="text-base font-medium">molma</div>
      <p class="text-sm text-muted-foreground">
        A home-server OS for people who want to own their data — not become sysadmins.
      </p>
      <a
        href="https://github.com/molmaos/molma"
        target="_blank"
        rel="noopener noreferrer"
        class="inline-block text-sm text-accent hover:underline"
      >
        Project on GitHub →
      </a>
    </div>
  </section>
</template>
