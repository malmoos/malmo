<script setup lang="ts">
// Settings → About — what this box is. Deliberately minimal: there is no
// version/box-name endpoint exposed to the UI yet, so this renders only what's
// genuinely known (product identity + project link) rather than fabricating
// system facts. When the brain exposes a build/version + box-name surface, this
// section grows to show it.
import Heading from "@/components/ui/Heading.vue";
</script>

<template>
  <section class="space-y-4">
    <Heading :level="2">About</Heading>
    <div class="space-y-2 rounded-2xl border border-border bg-card p-5">
      <div class="text-base font-medium">malmo</div>
      <p class="text-sm text-muted-foreground">
        A home-server OS for people who want to own their data — not become sysadmins.
      </p>
      <a
        href="https://github.com/malmoos/malmo"
        target="_blank"
        rel="noopener noreferrer"
        class="inline-block text-sm text-accent hover:underline"
      >
        Project on GitHub →
      </a>
    </div>
  </section>
</template>
