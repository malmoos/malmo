<script setup lang="ts">
// The floating bottom dock — exactly four destinations (DASHBOARD.md # global
// navigation): Home, Files, Store, Settings. Activity and Users are *not* here;
// they live under Settings as role-gated routes. RouterLink's active class
// drives the highlight; `exact-active` on Home avoids it matching every route.
import { RouterLink } from "vue-router";
import { House, FolderOpen, Store, Settings, type LucideIcon } from "lucide-vue-next";

const items: { to: string; label: string; icon: LucideIcon; exact?: boolean }[] = [
  { to: "/", label: "Home", icon: House, exact: true },
  { to: "/files", label: "Files", icon: FolderOpen },
  { to: "/store", label: "Store", icon: Store },
  { to: "/settings", label: "Settings", icon: Settings },
];
</script>

<template>
  <nav
    class="fixed inset-x-0 bottom-4 z-20 mx-auto flex w-fit items-center gap-1 rounded-2xl border border-border bg-card/90 px-2 py-1.5 shadow-lg backdrop-blur"
  >
    <RouterLink
      v-for="item in items"
      :key="item.to"
      :to="item.to"
      :active-class="item.exact ? '' : 'text-accent'"
      :exact-active-class="item.exact ? 'text-accent' : ''"
      class="flex min-w-16 flex-col items-center gap-0.5 rounded-xl px-3 py-1.5 text-muted-foreground hover:bg-muted"
    >
      <component :is="item.icon" class="size-5" />
      <span class="text-[11px]">{{ item.label }}</span>
    </RouterLink>
  </nav>
</template>
