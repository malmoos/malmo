// Vue Router 4, history mode (WEB_UI.md: Caddy serves index.html for unmatched
// routes). The four destinations mirror the dock in DASHBOARD.md # global
// navigation. Activity and Users live *under* Settings as role-gated routes
// (not built yet); they'll nest here when AUTH.md gating lands.
import { createRouter, createWebHistory, type RouteRecordRaw } from "vue-router";

const routes: RouteRecordRaw[] = [
  { path: "/", name: "home", component: () => import("@/views/HomeView.vue") },
  { path: "/files", name: "files", component: () => import("@/views/FilesView.vue") },
  { path: "/store", name: "store", component: () => import("@/views/StoreView.vue") },
  // Door-2 custom-container install — a dedicated full-screen form, admin-only
  // (the view guards the role; the Store affordance is hidden from members).
  { path: "/store/custom", name: "store-custom", component: () => import("@/views/CustomInstallView.vue") },
  { path: "/settings", name: "settings", component: () => import("@/views/SettingsView.vue") },
  // Admin-only sub-routes under Settings (DASHBOARD.md # global navigation,
  // AUTH.md # Roles). The views guard the role directly (CustomInstallView pattern).
  { path: "/settings/users", name: "settings-users", component: () => import("@/views/UsersView.vue") },
  // Unknown paths fall back to Home — the SPA never 404s its own chrome.
  { path: "/:pathMatch(.*)*", redirect: "/" },
];

export const router = createRouter({
  history: createWebHistory(),
  routes,
  // Scroll to a #hash target when the destination carries one (the degraded-mode
  // banner links to #health-issues on Home); other navigations keep the default
  // (no forced scroll), so existing behavior is unchanged.
  scrollBehavior(to) {
    if (to.hash) return { el: to.hash, behavior: "smooth" };
  },
});
