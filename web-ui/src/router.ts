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
  // Sub-routes under Settings (DASHBOARD.md # global navigation, AUTH.md # Roles).
  // Users is admin-only (the view guards the role, CustomInstallView pattern);
  // Activity is open to all authenticated users — members see only their own
  // events, enforced server-side (LOGGING.md # Visibility rules).
  { path: "/settings/users", name: "settings-users", component: () => import("@/views/UsersView.vue") },
  { path: "/settings/activity", name: "settings-activity", component: () => import("@/views/ActivityView.vue") },
  // Recovery is reachable while logged OUT (AUTH.md # Using the recovery code).
  // It's registered here so the catch-all below doesn't redirect the path away;
  // App.vue renders RecoverView directly in its logged-out branch, since the
  // router-view this route would feed only mounts inside AppShell once signed in.
  { path: "/recover", name: "recover", component: () => import("@/RecoverView.vue") },
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
