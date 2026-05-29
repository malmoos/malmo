// Vue Router 4, history mode (WEB_UI.md: Caddy serves index.html for unmatched
// routes). The four destinations mirror the dock in DASHBOARD.md # global
// navigation. Activity and Users live *under* Settings as role-gated routes
// (not built yet); they'll nest here when AUTH.md gating lands.
import { createRouter, createWebHistory, type RouteRecordRaw } from "vue-router";

const routes: RouteRecordRaw[] = [
  { path: "/", name: "home", component: () => import("@/views/HomeView.vue") },
  { path: "/files", name: "files", component: () => import("@/views/FilesView.vue") },
  { path: "/store", name: "store", component: () => import("@/views/StoreView.vue") },
  { path: "/settings", name: "settings", component: () => import("@/views/SettingsView.vue") },
  // Unknown paths fall back to Home — the SPA never 404s its own chrome.
  { path: "/:pathMatch(.*)*", redirect: "/" },
];

export const router = createRouter({
  history: createWebHistory(),
  routes,
});
