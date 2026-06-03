<script setup lang="ts">
import { ref } from "vue";
import { login } from "./auth";
import type { ApiError } from "./api";

const username = ref("");
const password = ref("");
const submitting = ref(false);
const error = ref("");

async function submit() {
  error.value = "";
  submitting.value = true;
  try {
    await login(username.value, password.value);
  } catch (e) {
    error.value = (e as ApiError).message || "Login failed";
  } finally {
    submitting.value = false;
  }
}
</script>

<template>
  <main class="auth">
    <h1>molma</h1>
    <form @submit.prevent="submit">
      <h2>Sign in</h2>
      <label>
        Username
        <input v-model="username" autocomplete="username" required autofocus />
      </label>
      <label>
        Password
        <input v-model="password" type="password" autocomplete="current-password" required />
      </label>
      <button type="submit" :disabled="submitting">
        {{ submitting ? "Signing in…" : "Sign in" }}
      </button>
      <p v-if="error" class="error">{{ error }}</p>
    </form>
  </main>
</template>

<style>
.auth { max-width: 360px; margin: 4rem auto; padding: 0 1rem; }
.auth h1 { font-size: 1.6rem; margin-bottom: 1.5rem; text-align: center; }
.auth form { background: #fff; border: 1px solid #e6e6e8; border-radius: 12px; padding: 1.5rem; display: flex; flex-direction: column; gap: 0.75rem; }
.auth h2 { margin: 0 0 0.5rem; font-size: 1rem; text-transform: uppercase; letter-spacing: 0.04em; color: #666; }
.auth label { display: flex; flex-direction: column; gap: 0.25rem; font-size: 0.85rem; color: #444; }
.auth input { border: 1px solid #ddd; border-radius: 8px; padding: 0.5rem 0.7rem; font-size: 0.95rem; }
.auth button { border: 1px solid #2b6cb0; background: #2b6cb0; color: #fff; border-radius: 8px; padding: 0.55rem 0.9rem; cursor: pointer; font-size: 0.9rem; margin-top: 0.5rem; }
.auth button:disabled { opacity: 0.6; cursor: default; }
.auth .error { color: #a11; font-size: 0.85rem; margin: 0; }
.auth .recovery { background: #fff8d9; border: 1px solid #e6d27a; border-radius: 8px; padding: 0.75rem; font-family: ui-monospace, monospace; font-size: 0.9rem; word-break: break-all; }
.auth .hint { color: #666; font-size: 0.8rem; margin: 0; }
</style>
