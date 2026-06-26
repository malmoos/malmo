<script setup lang="ts">
// First-run wizard, step 1 — create the founding admin (FIRST_RUN.md # Step 2 +
// Step 2a). On the hosted profile this step also collects the one-time
// admin-bootstrap secret the operator pastes (ENVIRONMENT.md # Provisioning,
// C3a's `bootstrap_secret`); the appliance profile ignores it.
//
// Recovery is on by default (FIRST_RUN.md # Step 2a). When kept on, the setup
// response carries the code once and we hold on a save-it screen until the user
// acknowledges — the brain stores only a hash, so this is the single reveal.
// When toggled off, the form shows the tradeoff copy and no code is generated.
//
// NOTE (known gap): the field is a Linux username, not the display-name → slug
// mapping FIRST_RUN.md # Identity & display names specs. That slugification is
// deferred; tracked in the progress entry.
import { onMounted, ref } from "vue";
import { setup, useAuth } from "../auth";
import type { ApiError } from "../api";

const emit = defineEmits<{ done: [] }>();
const { profile } = useAuth();

const username = ref("");
const password = ref("");
const bootstrapSecret = ref("");
// Focused after a link-prefill so the operator lands on the username field
// instead of the already-filled secret field (which keeps its autofocus for the
// manual-paste path).
const usernameInput = ref<HTMLInputElement>();

// The one-time setup secret is a fixed-shape token: 43 base64url characters
// (the cloud mints 32 random bytes, base64url without padding). Validating that
// shape before we hit /setup turns the most common failure — a partial paste —
// into an actionable message instead of an opaque 401 from the bootstrap gate.
const secretShape = /^[A-Za-z0-9_-]{43}$/;

// The portal links here with the secret in the URL fragment
// (<box>/setup#secret=...), so the operator never re-types it. A fragment is
// never sent to the server, so reading it here keeps the secret out of the
// access log and Referer. Prefill the field, strip the hash from the address
// bar (and history) so the secret does not linger on screen, then move focus to
// the username so the prefilled happy path needs no extra tab. Parse with
// URLSearchParams (not decodeURIComponent) so any percent-encoded byte the
// portal sent round-trips correctly; base64url carries no '+', so the
// form-decoding of '+' as space cannot bite the current token shape.
onMounted(() => {
  const fromLink = new URLSearchParams(location.hash.replace(/^#/, "")).get("secret");
  if (fromLink) {
    bootstrapSecret.value = fromLink;
    history.replaceState(null, "", location.pathname + location.search);
    usernameInput.value?.focus();
  }
});

const recovery = ref(true);
const submitting = ref(false);
const error = ref("");
// Held after a successful setup when recovery was on: the code to show once.
const recoveryCode = ref<string | null>(null);
// Save-it screen gating (FIRST_RUN.md # Step 2a): Continue stays disabled until
// the user ticks the acknowledgement, so the single-reveal code isn't skipped past.
const acknowledged = ref(false);
const copied = ref(false);

// copyCode copies the recovery code to the clipboard. The Clipboard API needs a
// secure context — true on hosted (HTTPS) but not on the appliance dashboard
// (http://*.local) — so fall back to a hidden-textarea execCommand there.
async function copyCode() {
  if (!recoveryCode.value) return;
  try {
    if (window.isSecureContext && navigator.clipboard) {
      await navigator.clipboard.writeText(recoveryCode.value);
    } else {
      const ta = document.createElement("textarea");
      ta.value = recoveryCode.value;
      ta.style.position = "fixed";
      ta.style.opacity = "0";
      document.body.appendChild(ta);
      ta.select();
      document.execCommand("copy");
      document.body.removeChild(ta);
    }
    copied.value = true;
    setTimeout(() => (copied.value = false), 2000);
  } catch {
    // Clipboard blocked — the code is still on screen to copy by hand.
  }
}

async function submit() {
  error.value = "";
  // Catch a malformed (usually truncated) setup secret before the round-trip,
  // so a bad paste gets a clear "doesn't look complete" instead of the gate's
  // generic 401. Hosted only — the appliance ignores the secret entirely.
  if (profile.value === "hosted" && !secretShape.test(bootstrapSecret.value.trim())) {
    error.value =
      "That doesn't look like a complete setup secret. Paste the full 43-character code shown when your box was created.";
    return;
  }
  submitting.value = true;
  try {
    const res = await setup(username.value.trim(), password.value, {
      recovery: recovery.value,
      bootstrapSecret: bootstrapSecret.value.trim() || undefined,
    });
    if (recovery.value && res.recovery_code) {
      recoveryCode.value = res.recovery_code; // show-once screen below
    } else {
      emit("done"); // recovery off → nothing to save, advance immediately
    }
  } catch (e) {
    error.value = (e as ApiError).message || "Setup failed";
  } finally {
    submitting.value = false;
  }
}
</script>

<template>
  <form v-if="!recoveryCode" @submit.prevent="submit">
    <h2>Create your admin account</h2>
    <p class="hint">This is the first account on the box — the administrator.</p>

    <label v-if="profile === 'hosted'">
      Setup secret
      <input v-model="bootstrapSecret" autocomplete="off" required autofocus />
    </label>
    <p v-if="profile === 'hosted'" class="hint">
      Paste the one-time setup secret shown when your box was created.
    </p>

    <label>
      Username
      <input
        ref="usernameInput"
        v-model="username"
        autocomplete="username"
        required
        :autofocus="profile !== 'hosted'"
      />
    </label>
    <label>
      Password
      <input v-model="password" type="password" autocomplete="new-password" required />
    </label>

    <label class="check">
      <input v-model="recovery" type="checkbox" />
      Save a recovery code (recommended)
    </label>
    <p v-if="!recovery" class="hint warn">
      You won't be able to recover your account if you forget your password.
      Continue without a recovery code?
    </p>

    <button type="submit" :disabled="submitting">
      {{ submitting ? "Creating…" : "Continue" }}
    </button>
    <p v-if="error" class="error">{{ error }}</p>
  </form>

  <form v-else @submit.prevent="emit('done')">
    <h2>Save your recovery code</h2>
    <p class="hint">
      Write this down or take a photo. If you forget your password, this code is
      the only way back in — we store only a hash, never the code itself.
    </p>
    <div class="recovery-row">
      <div class="recovery">{{ recoveryCode }}</div>
      <button type="button" class="copy" @click="copyCode">
        {{ copied ? "Copied" : "Copy" }}
      </button>
    </div>

    <label class="check">
      <input v-model="acknowledged" type="checkbox" />
      I have saved this recovery code
    </label>

    <button type="submit" :disabled="!acknowledged">Continue</button>
  </form>
</template>
