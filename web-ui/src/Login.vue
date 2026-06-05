<script setup lang="ts">
// Login screen — AUTH.md # Login screen UX: user-list style (macOS / Plex / Synology
// pattern). Users are fetched from GET /auth/users (public). Click a name →
// password field appears → submit. No username text field.
import { ref } from "vue";
import { RouterLink } from "vue-router";
import { useQuery } from "@tanstack/vue-query";
import { api, type ApiError } from "./api";
import { login } from "./auth";

interface PickerUser { id: string; username: string; }

// Public endpoint: lists users for the login picker (no session required).
const users = useQuery({
  queryKey: ["auth-users"],
  queryFn: () => api.get<{ users: PickerUser[] }>("/auth/users"),
});

const selected = ref<PickerUser | null>(null);
const password = ref("");
const submitting = ref(false);
const error = ref("");

function pick(u: PickerUser) {
  selected.value = u;
  password.value = "";
  error.value = "";
}

function back() {
  selected.value = null;
  password.value = "";
  error.value = "";
}

async function submit() {
  if (!selected.value) return;
  error.value = "";
  submitting.value = true;
  try {
    await login(selected.value.username, password.value);
  } catch (e) {
    error.value = (e as ApiError).message || "Incorrect password.";
  } finally {
    submitting.value = false;
  }
}

// Letter glyph color — deterministic per username so it's stable across reloads.
const GLYPHS = ["#4f86c6", "#e07b4f", "#5aab6e", "#a06bc4", "#c45a7b", "#6babc4"];
function glyphColor(username: string): string {
  let h = 0;
  for (let i = 0; i < username.length; i++) h = (h * 31 + username.charCodeAt(i)) >>> 0;
  return GLYPHS[h % GLYPHS.length]!;
}
</script>

<template>
  <main class="auth">
    <h1>molma</h1>

    <!-- User picker -->
    <div v-if="!selected" class="card">
      <h2>Who are you?</h2>
      <p v-if="users.isLoading.value" class="hint">Loading…</p>
      <ul v-else class="user-list">
        <li
          v-for="u in (users.data.value?.users ?? [])"
          :key="u.id"
          class="user-item"
          @click="pick(u)"
        >
          <span class="glyph" :style="{ background: glyphColor(u.username) }">
            {{ u.username[0]!.toUpperCase() }}
          </span>
          <span class="name">{{ u.username }}</span>
        </li>
      </ul>
    </div>

    <!-- Password entry -->
    <form v-else class="card" @submit.prevent="submit">
      <button type="button" class="back" @click="back">← Back</button>
      <div class="selected-user">
        <span class="glyph" :style="{ background: glyphColor(selected.username) }">
          {{ selected.username[0]!.toUpperCase() }}
        </span>
        <span class="name">{{ selected.username }}</span>
      </div>
      <label>
        Password
        <input
          v-model="password"
          type="password"
          autocomplete="current-password"
          required
          autofocus
        />
      </label>
      <button type="submit" :disabled="submitting">
        {{ submitting ? "Signing in…" : "Sign in" }}
      </button>
      <p v-if="error" class="error">{{ error }}</p>
      <RouterLink to="/recover" class="forgot">Forgot password?</RouterLink>
    </form>
  </main>
</template>

<style>
/* Login-specific styles (auth base styles live in style.css). */
.auth .user-list { list-style: none; margin: 0; padding: 0; display: flex; flex-direction: column; gap: 0.5rem; }
.auth .user-item { display: flex; align-items: center; gap: 0.75rem; padding: 0.6rem 0.75rem; border-radius: 8px; cursor: pointer; }
.auth .user-item:hover { background: #f4f4f6; }
.auth .glyph { width: 2rem; height: 2rem; border-radius: 50%; display: flex; align-items: center; justify-content: center; font-size: 0.95rem; font-weight: 600; color: #fff; flex-shrink: 0; }
.auth .name { font-size: 0.95rem; }
.auth .selected-user { display: flex; align-items: center; gap: 0.75rem; padding: 0.25rem 0; }
.auth .back { align-self: flex-start; background: none; border: none; color: #666; font-size: 0.85rem; cursor: pointer; padding: 0; margin-bottom: 0.25rem; }
.auth .back:hover { color: #222; }
.auth .forgot { align-self: center; color: #666; font-size: 0.85rem; text-decoration: none; margin-top: 0.25rem; }
.auth .forgot:hover { color: #222; }
</style>
