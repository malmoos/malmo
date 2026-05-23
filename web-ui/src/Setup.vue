<script setup lang="ts">
import { ref } from "vue";
import { setup, setupComplete } from "./auth";
import type { ApiError } from "./api";

// FIRST_RUN.md # Step 2 says the user types a first name + password and
// display-name is shown everywhere. For the single-user walking-skeleton
// phase we skip the name entirely — only a password — and hardcode the slug
// to "admin". The multi-user UI (and the user-list login screen from
// AUTH.md # Login screen UX) is downstream of this.
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
    const res = await setup("admin", password.value);
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
      <h2>Set your password</h2>
      <p class="hint">This is the only account on the box — the admin.</p>
      <label>
        Password
        <input v-model="password" type="password" autocomplete="new-password" required autofocus />
      </label>
      <button type="submit" :disabled="submitting">
        {{ submitting ? "Creating…" : "Continue" }}
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
