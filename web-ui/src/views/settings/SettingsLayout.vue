<script setup lang="ts">
// Settings shell — a left nav rail plus a routed content pane (DASHBOARD.md #
// global navigation: Settings is "box + account settings, and the home for gated
// routes"). The internal IA is a two-pane layout: ~1/3 nav on the left, the
// active section's <RouterView> filling the rest. Each section is its own nested
// route under /settings, so deep-links and the avatar-menu links keep working.
//
// Role gating mirrors AUTH.md: Users is admin-only (hidden from members here;
// the section view also redirects, defence in depth). Everything else is open to
// every signed-in user.
//
// Responsive: the rail is a sidebar on md+ and collapses to a horizontal,
// scrollable tab strip above the content on narrow screens — the dock is
// mobile-first, so Settings must degrade the same way.
import { computed } from "vue";
import { RouterLink, RouterView } from "vue-router";
import { User, Bell, LayoutGrid, ScrollText, Users, Info, type LucideIcon } from "lucide-vue-next";
import { useAuth } from "@/auth";

const { currentUser } = useAuth();

type NavItem = { to: string; label: string; icon: LucideIcon; adminOnly?: boolean };

// Order is the menu order. Account first, then Users (admins land on people
// management right after their own account); the rest follows, About last.
const allItems: NavItem[] = [
  { to: "/settings/account", label: "Account", icon: User },
  { to: "/settings/users", label: "Users", icon: Users, adminOnly: true },
  { to: "/settings/notifications", label: "Notifications", icon: Bell },
  { to: "/settings/apps", label: "Installed apps", icon: LayoutGrid },
  { to: "/settings/activity", label: "Activity", icon: ScrollText },
  { to: "/settings/about", label: "About", icon: Info },
];

const items = computed(() =>
  allItems.filter((i) => !i.adminOnly || currentUser.value?.role === "admin"),
);
</script>

<template>
  <div class="flex flex-col gap-6 pt-2 md:flex-row md:gap-8">
    <!-- Left nav. Horizontal scroll strip on narrow screens, sticky rail on md+. -->
    <nav
      class="-mx-1 flex shrink-0 gap-1 overflow-x-auto px-1 md:mx-0 md:w-56 md:flex-col md:overflow-visible md:px-0"
    >
      <RouterLink
        v-for="item in items"
        :key="item.to"
        :to="item.to"
        active-class="bg-muted text-foreground"
        class="flex items-center gap-2.5 whitespace-nowrap rounded-xl px-3 py-2 text-sm text-muted-foreground hover:bg-muted hover:text-foreground"
      >
        <component :is="item.icon" class="size-4 shrink-0" />
        <span>{{ item.label }}</span>
      </RouterLink>
    </nav>

    <!-- Content pane — the active section. -->
    <div class="min-w-0 flex-1">
      <RouterView />
    </div>
  </div>
</template>
