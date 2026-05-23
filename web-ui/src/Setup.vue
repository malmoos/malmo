<script setup lang="ts">
import { ref } from "vue";
import { setup, setupComplete } from "./auth";
import type { ApiError } from "./api";

const username = ref("");
const password = ref("");
const submitting = ref(false);
const error = ref("");
// AUTH.md # Recovery: the recovery code is shown exactly once. We display it
// after setup succeeds and require an explicit "I've saved it" click before
// continuing — the brain has only the hash, so this is the user's one chance.
const recoveryCode = ref<string | null>(null);

async function submit() {
  error.value = "";
  submitting.value = true;
  try {
    const res = await setup(username.value, password.value);
    recoveryCode.value = res.recovery_code;
  } catch (e) {
    error.value = (e as ApiError).message || "Setup failed";
  } finally {
    submitting.value = false;
  }
}

function acknowledge() {
  setupComplete();
}
</script>

<template>
  <main class="auth">
    <h1>malmo</h1>
    <form v-if="!recoveryCode" @submit.prevent="submit">
      <h2>Create the first admin</h2>
      <p class="hint">This account can install apps, add users, and recover the box.</p>
      <label>
        Username
        <input v-model="username" autocomplete="username" required autofocus />
      </label>
      <label>
        Password
        <input v-model="password" type="password" autocomplete="new-password" required />
      </label>
      <button type="submit" :disabled="submitting">
        {{ submitting ? "Creating…" : "Create admin" }}
      </button>
      <p v-if="error" class="error">{{ error }}</p>
    </form>

    <form v-else @submit.prevent="acknowledge">
      <h2>Save your recovery code</h2>
      <p class="hint">
        Write this down. If you lose your password, this is the only way back in —
        we don't store the code itself, just a hash.
      </p>
      <div class="recovery">{{ recoveryCode }}</div>
      <button type="submit">I've saved it — continue</button>
    </form>
  </main>
</template>
