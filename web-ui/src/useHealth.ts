// useHealth wraps GET /api/v1/health (HEALTH.md # Display) as TanStack Query
// state — the single source of truth for the degraded-mode banner and the
// <HealthGated> affordances (issue #12). Every caller shares one cached query
// (keyed ["health/issues"]), so the banner, the gated buttons, and the Home
// issues list read the same active-issue set without each opening its own
// request. That shared-cache property is exactly why this is a composable over
// Query and not a Pinia store: WEB_UI.md # State locks "server state lives in
// Query, not Pinia — Pinia is reserved for genuinely client-side state," and
// active issues are server state. useEvents pushes a cache invalidation on
// health.issue_raised / health.issue_cleared so the set refreshes live.
//
// The endpoint is admin-only in v1 (internal/api/health.go returns 403 to
// members), so the query is gated on isAdmin — a member's dashboard never
// fires it and never sees the banner. Widening to a member-facing health read
// is issue #12's deliberately-deferred follow-up.
import { computed } from "vue";
import { useQuery } from "@tanstack/vue-query";
import { api, type HealthIssue } from "./api";
import { useAuth } from "./auth";

export function useHealth() {
  const { currentUser } = useAuth();
  const isAdmin = computed(() => currentUser.value?.role === "admin");

  const query = useQuery({
    queryKey: ["health/issues"],
    queryFn: () => api.get<{ issues: HealthIssue[] }>("/health"),
    enabled: isAdmin,
  });

  const activeIssues = computed<HealthIssue[]>(() => query.data.value?.issues ?? []);

  // The global banner shows only for error/critical issues (HEALTH.md #
  // Display: "a global banner ... when any critical or error issue is active").
  // Warnings surface as inline cards on the relevant page — a follow-up.
  const blockingIssues = computed(() =>
    activeIssues.value.filter((i) => i.severity === "error" || i.severity === "critical"),
  );

  // blocks_* gates: true when ANY active issue sets the flag, mirroring the
  // brain's own gate (HEALTH.md: the operation gate is !any(i.blocks_X)).
  // <HealthGated> reads these to disable affordances.
  const blocksApps = computed(() => activeIssues.value.some((i) => i.blocks_apps));
  const blocksWrites = computed(() => activeIssues.value.some((i) => i.blocks_writes));
  const blocksUsers = computed(() => activeIssues.value.some((i) => i.blocks_users));

  return { activeIssues, blockingIssues, blocksApps, blocksWrites, blocksUsers, isAdmin };
}
