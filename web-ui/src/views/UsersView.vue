<script setup lang="ts">
// Settings → Users — admin-only user management (USERS_AND_GROUPS.md, issue #10).
// Consumes: GET /users, POST /users, PATCH /users/{id} (role), DELETE /users/{id},
// POST /users/{id}/password. Guard rejections (409 last-admin, 409 self-delete/self-
// demotion, 409 duplicate) surface as inline error messages; controls are never
// hidden. Every mutation is wrapped in withElevation: the brain requires a 5-minute
// elevation window for these ops (USERS_AND_GROUPS.md # Elevation in the UI), so the
// first mutation re-prompts for the password via ElevateDialog and retries.
import { ref, watch } from "vue";
import { RouterLink, useRouter } from "vue-router";
import { useQuery, useMutation, useQueryClient } from "@tanstack/vue-query";
import { api, ApiError, type User } from "../api";
import { withElevation } from "../elevate";
import { useAuth } from "../auth";

const router = useRouter();
const qc = useQueryClient();
const { currentUser, refreshCurrentUser } = useAuth();

// Admin-only — redirect members immediately (mirrors CustomInstallView pattern).
watch(
  currentUser,
  (u) => {
    if (u && u.role !== "admin") router.replace("/settings");
  },
  { immediate: true },
);

// ── user list ──────────────────────────────────────────────────────────────────
const users = useQuery({
  queryKey: ["users"],
  queryFn: () => api.get<{ users: User[] }>("/users"),
});

// ── create form ────────────────────────────────────────────────────────────────
const newUsername = ref("");
const newPassword = ref("");
const newRole = ref<"member" | "admin">("member");
const createError = ref<string | null>(null);

const create = useMutation({
  mutationFn: () =>
    withElevation(() =>
      api.post<User>("/users", {
        username: newUsername.value.trim(),
        password: newPassword.value,
        role: newRole.value,
      }),
    ),
  onSuccess: () => {
    newUsername.value = "";
    newPassword.value = "";
    newRole.value = "member";
    createError.value = null;
    qc.invalidateQueries({ queryKey: ["users"] });
    refreshCurrentUser();
  },
  onError: (e) => {
    createError.value = errorMessage(e);
  },
});

// ── per-row state ──────────────────────────────────────────────────────────────
// id of the user whose reset-password form is expanded (only one at a time)
const resetFor = ref<string | null>(null);
const resetPasswordValue = ref("");

// id of the user pending delete confirmation (destructive op — confirm before
// the elevation prompt; USERS_AND_GROUPS.md # Elevation in the UI).
const confirmDeleteFor = ref<string | null>(null);

// per-user inline error message (role change, delete, reset-password)
const rowError = ref<Record<string, string>>({});

function clearRowError(id: string) {
  const next = { ...rowError.value };
  delete next[id];
  rowError.value = next;
}

function errorMessage(e: unknown): string {
  // A dismissed elevation prompt is a deliberate no-op, not a failure.
  if (e instanceof ApiError && e.code === "elevation_cancelled") return "";
  return e instanceof ApiError ? e.message : "Something went wrong.";
}

function setRowError(id: string, e: unknown) {
  rowError.value = { ...rowError.value, [id]: errorMessage(e) };
}

// ── change role ────────────────────────────────────────────────────────────────
const changeRole = useMutation({
  mutationFn: ({ id, role }: { id: string; role: string }) =>
    withElevation(() => api.patch<User>(`/users/${id}`, { role })),
  onSuccess: (_, { id }) => {
    clearRowError(id);
    qc.invalidateQueries({ queryKey: ["users"] });
    refreshCurrentUser();
  },
  // On failure (guard rejection, cancelled elevation) the <select> is showing
  // the value the user picked, which the server rejected — refetch so it snaps
  // back to the real role.
  onError: (e, { id }) => {
    setRowError(id, e);
    qc.invalidateQueries({ queryKey: ["users"] });
  },
});

// ── delete user ────────────────────────────────────────────────────────────────
const deleteUser = useMutation({
  mutationFn: (id: string) => withElevation(() => api.del<void>(`/users/${id}`)),
  onSuccess: (_, id) => {
    clearRowError(id);
    confirmDeleteFor.value = null;
    qc.invalidateQueries({ queryKey: ["users"] });
    refreshCurrentUser();
  },
  onError: (e, id) => setRowError(id, e),
});

// ── reset password ─────────────────────────────────────────────────────────────
const doResetPassword = useMutation({
  mutationFn: ({ id, password }: { id: string; password: string }) =>
    withElevation(() => api.post<User>(`/users/${id}/password`, { password })),
  onSuccess: (_, { id }) => {
    clearRowError(id);
    resetFor.value = null;
    resetPasswordValue.value = "";
  },
  onError: (e, { id }) => setRowError(id, e),
});
</script>

<template>
  <div class="space-y-6 pt-2">
    <div class="flex items-center gap-2">
      <RouterLink to="/settings" class="text-sm text-muted-foreground hover:text-foreground">← Settings</RouterLink>
      <span class="text-sm text-muted-foreground">/</span>
      <span class="text-sm font-medium">Users</span>
    </div>

    <!-- Create user form -->
    <section class="space-y-3">
      <h2 class="text-xs font-medium uppercase tracking-wide text-muted-foreground">Add user</h2>
      <div class="rounded-xl border border-border bg-card px-4 py-3 space-y-3">
        <div class="flex flex-wrap gap-2">
          <input
            v-model="newUsername"
            placeholder="Username"
            class="min-w-28 flex-1 rounded-lg border border-border bg-background px-3 py-1.5 text-sm outline-none focus:border-accent"
            autocomplete="off"
            @keydown.enter="!create.isPending.value && newUsername.trim() && newPassword && create.mutate()"
          />
          <input
            v-model="newPassword"
            type="password"
            placeholder="Password"
            class="min-w-28 flex-1 rounded-lg border border-border bg-background px-3 py-1.5 text-sm outline-none focus:border-accent"
            autocomplete="new-password"
            @keydown.enter="!create.isPending.value && newUsername.trim() && newPassword && create.mutate()"
          />
          <select
            v-model="newRole"
            class="rounded-lg border border-border bg-background px-3 py-1.5 text-sm outline-none focus:border-accent"
          >
            <option value="member">Member</option>
            <option value="admin">Admin</option>
          </select>
          <button
            class="rounded-lg border border-border px-4 py-1.5 text-sm hover:bg-muted disabled:opacity-50"
            :disabled="create.isPending.value || !newUsername.trim() || !newPassword"
            @click="create.mutate()"
          >
            Add
          </button>
        </div>
        <p v-if="createError" class="text-xs text-destructive">{{ createError }}</p>
      </div>
    </section>

    <!-- User list -->
    <section class="space-y-3">
      <h2 class="text-xs font-medium uppercase tracking-wide text-muted-foreground">People</h2>
      <p v-if="users.isLoading.value" class="text-sm text-muted-foreground">Loading…</p>
      <ul v-else class="space-y-2">
        <li
          v-for="u in (users.data.value?.users ?? [])"
          :key="u.id"
          class="rounded-xl border border-border bg-card px-4 py-3 space-y-3"
        >
          <!-- Main row -->
          <div class="flex flex-wrap items-center gap-2">
            <div class="min-w-0 flex-1">
              <span class="text-sm font-medium">{{ u.username }}</span>
              <span v-if="u.id === currentUser?.id" class="ml-1.5 text-xs text-muted-foreground">(you)</span>
            </div>
            <!-- Role -->
            <select
              :value="u.role"
              class="rounded-lg border border-border bg-background px-2 py-1 text-sm outline-none focus:border-accent disabled:opacity-50"
              :disabled="changeRole.isPending.value"
              @change="(e) => changeRole.mutate({ id: u.id, role: (e.target as HTMLSelectElement).value })"
            >
              <option value="member">Member</option>
              <option value="admin">Admin</option>
            </select>
            <!-- Reset password toggle -->
            <button
              class="rounded-lg border border-border px-3 py-1 text-sm hover:bg-muted"
              @click="resetFor = resetFor === u.id ? null : u.id; resetPasswordValue = ''"
            >
              Reset password
            </button>
            <!-- Delete -->
            <button
              class="rounded-lg border border-border px-3 py-1 text-sm text-destructive hover:bg-muted disabled:opacity-50"
              :disabled="deleteUser.isPending.value"
              @click="confirmDeleteFor = confirmDeleteFor === u.id ? null : u.id"
            >
              Delete
            </button>
          </div>

          <!-- Delete confirmation (irreversible — confirm before mutating) -->
          <div
            v-if="confirmDeleteFor === u.id"
            class="flex flex-wrap items-center gap-2 rounded-lg border border-destructive/40 bg-destructive/5 px-3 py-2"
          >
            <span class="text-sm">Delete <strong>{{ u.username }}</strong>? This can't be undone.</span>
            <button
              class="rounded-lg border border-destructive px-3 py-1 text-sm text-destructive hover:bg-destructive/10 disabled:opacity-50"
              :disabled="deleteUser.isPending.value"
              @click="deleteUser.mutate(u.id)"
            >
              Delete
            </button>
            <button
              class="rounded-lg border border-border px-3 py-1 text-sm hover:bg-muted"
              @click="confirmDeleteFor = null"
            >
              Cancel
            </button>
          </div>

          <!-- Inline reset-password form (expands on toggle) -->
          <div v-if="resetFor === u.id" class="flex flex-wrap gap-2">
            <input
              v-model="resetPasswordValue"
              type="password"
              placeholder="New password"
              class="min-w-40 flex-1 rounded-lg border border-border bg-background px-3 py-1.5 text-sm outline-none focus:border-accent"
              autocomplete="new-password"
              @keydown.enter="!doResetPassword.isPending.value && resetPasswordValue && doResetPassword.mutate({ id: u.id, password: resetPasswordValue })"
            />
            <button
              class="rounded-lg border border-border px-3 py-1.5 text-sm hover:bg-muted disabled:opacity-50"
              :disabled="doResetPassword.isPending.value || !resetPasswordValue"
              @click="doResetPassword.mutate({ id: u.id, password: resetPasswordValue })"
            >
              Save
            </button>
            <button
              class="rounded-lg border border-border px-3 py-1.5 text-sm hover:bg-muted"
              @click="resetFor = null; resetPasswordValue = ''"
            >
              Cancel
            </button>
          </div>

          <!-- Per-row guard / error message -->
          <p v-if="rowError[u.id]" class="text-xs text-destructive">{{ rowError[u.id] }}</p>
        </li>
      </ul>
    </section>
  </div>
</template>
