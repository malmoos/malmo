<script setup lang="ts">
// First-run Step 2 + 2a (FIRST_RUN.md): create the first admin, then surface the
// recovery code. Real username + password (no hardcoded "admin"). The recovery
// toggle is on by default; turning it off requires the explicit Step 2a
// confirmation. On hosted, the admin-bootstrap secret the operator received
// out-of-band is forwarded to the C3a-gated /setup (ENVIRONMENT.md # Provisioning).
import { ref } from "vue";
import { setup } from "../auth";
import type { ApiError } from "../api";

const props = defineProps<{ profile: string }>();
const emit = defineEmits<{ next: [] }>();

const isHosted = props.profile === "hosted";

const username = ref("");
const password = ref("");
const secret = ref("");
// On by default (Step 2a). When off, an explicit acknowledgment is required.
const saveRecovery = ref(true);
const acknowledgedNoRecovery = ref(false);

const submitting = ref(false);
const error = ref("");
// Shown once after a successful /setup when a recovery code was generated.
const recoveryCode = ref<string | null>(null);
const savedRecovery = ref(false);
const copied = ref(false);

async function submit() {
  error.value = "";
  submitting.value = true;
  try {
    const res = await setup(username.value, password.value, {
      skipRecovery: !saveRecovery.value,
      secret: isHosted ? secret.value : undefined,
    });
    if (res.recovery_code) {
      // Show the code once; the user must confirm they saved it before advancing.
      recoveryCode.value = res.recovery_code;
    } else {
      // Opted out — no code to show; move straight on.
      emit("next");
    }
  } catch (e) {
    error.value = (e as ApiError).message || "Setup failed";
  } finally {
    submitting.value = false;
  }
}

async function copyCode() {
  if (!recoveryCode.value) return;
  try {
    await navigator.clipboard.writeText(recoveryCode.value);
    copied.value = true;
  } catch {
    // Clipboard can be blocked (insecure context); the code is visible to copy by hand.
  }
}
</script>

<template>
  <!-- Recovery-code reveal (shown once after setup succeeds with a code). -->
  <form v-if="recoveryCode" class="card" @submit.prevent="emit('next')">
    <h2>Save your recovery code</h2>
    <p class="hint">
      If you forget your dashboard password, this code is the only way back in. Take a photo of it —
      we store only a hash, so this is your one chance to save it.
    </p>
    <div class="recovery">{{ recoveryCode }}</div>
    <button type="button" class="secondary" @click="copyCode">
      {{ copied ? "Copied" : "Copy code" }}
    </button>
    <label class="check">
      <input v-model="savedRecovery" type="checkbox" />
      I have saved this code
    </label>
    <button type="submit" :disabled="!savedRecovery">Continue</button>
  </form>

  <!-- Admin creation form. -->
  <form v-else class="card" @submit.prevent="submit">
    <h2>Create your account</h2>
    <p class="hint">This first account is the box admin.</p>
    <label>
      Username
      <input v-model="username" type="text" autocomplete="username" required autofocus />
    </label>
    <label>
      Password
      <input v-model="password" type="password" autocomplete="new-password" required />
    </label>

    <label v-if="isHosted">
      Setup secret
      <input v-model="secret" type="text" autocomplete="off" required />
      <span class="sub">The one-time secret from your cloud console.</span>
    </label>

    <label class="check">
      <input v-model="saveRecovery" type="checkbox" />
      Save a recovery code (recommended)
    </label>

    <!-- Step 2a: turning the toggle off forces acknowledgment of the tradeoff. -->
    <div v-if="!saveRecovery" class="warn">
      <p>You won't be able to recover your account if you forget your password.</p>
      <label class="check">
        <input v-model="acknowledgedNoRecovery" type="checkbox" />
        Continue without a recovery code
      </label>
    </div>

    <button type="submit" :disabled="submitting || (!saveRecovery && !acknowledgedNoRecovery)">
      {{ submitting ? "Creating…" : "Continue" }}
    </button>
    <p v-if="error" class="error">{{ error }}</p>
  </form>
</template>

<style>
.auth .check { flex-direction: row; align-items: center; gap: 0.5rem; font-size: 0.85rem; color: #444; }
.auth .check input { width: auto; }
.auth .sub { color: #888; font-size: 0.75rem; }
.auth .warn { background: #fff3f3; border: 1px solid #f0c2c2; border-radius: 8px; padding: 0.6rem 0.75rem; font-size: 0.8rem; color: #8a3a3a; display: flex; flex-direction: column; gap: 0.4rem; }
.auth .warn p { margin: 0; }
.auth button.secondary { border: 1px solid #ccc; background: #fff; color: #333; border-radius: 8px; padding: 0.45rem 0.9rem; cursor: pointer; font-size: 0.85rem; }
</style>
