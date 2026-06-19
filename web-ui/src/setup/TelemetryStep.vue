<script setup lang="ts">
// First-run Step 4 — telemetry (FIRST_RUN.md, TELEMETRY.md). One unchecked
// checkbox, off by default, covering both the usage and crash streams. The
// inline disclosure carries the locked TELEMETRY.md # Backend-choice wording
// (PostHog must be named) plus a plain-language summary of the field allowlist.
// The founding admin makes this box-wide choice once; it's persisted in the brain.
import { ref } from "vue";
import { api, type ApiError } from "../api";

const emit = defineEmits<{ next: [] }>();

const enabled = ref(false);
const showDetails = ref(false);
const submitting = ref(false);
const error = ref("");

async function submit() {
  error.value = "";
  submitting.value = true;
  try {
    await api.post("/telemetry/consent", { enabled: enabled.value });
    emit("next");
  } catch (e) {
    error.value = (e as ApiError).message || "Could not save your choice";
  } finally {
    submitting.value = false;
  }
}
</script>

<template>
  <form class="card" @submit.prevent="submit">
    <h2>Help improve malmo</h2>
    <label class="check">
      <input v-model="enabled" type="checkbox" />
      Send anonymous usage statistics and crash reports to help improve malmo.
    </label>

    <button type="button" class="disclose" @click="showDetails = !showDetails">
      {{ showDetails ? "Hide details" : "What does this collect?" }}
    </button>
    <div v-if="showDetails" class="details">
      <p>
        Anonymous usage data and crash reports are sent to malmo, processed via PostHog (a third-party
        analytics provider). No identifying information is included.
      </p>
      <p>
        Collected: malmo and app versions, store-app install/uninstall and update outcomes, health and
        crash events, and coarse box facts (country, CPU/RAM size buckets).
      </p>
      <p>
        Never collected: your files, file paths, IP addresses, user accounts, per-app usage, or the names
        of apps you add yourself.
      </p>
      <p class="sub">You can change this anytime in Settings → Privacy.</p>
    </div>

    <button type="submit" :disabled="submitting">
      {{ submitting ? "Saving…" : "Continue" }}
    </button>
    <p v-if="error" class="error">{{ error }}</p>
  </form>
</template>

<style>
.auth button.disclose { align-self: flex-start; background: none; border: none; color: #2b6cb0; font-size: 0.8rem; cursor: pointer; padding: 0; text-decoration: underline; }
.auth .details { background: #f7f8fa; border: 1px solid #e6e6e8; border-radius: 8px; padding: 0.6rem 0.75rem; font-size: 0.78rem; color: #555; display: flex; flex-direction: column; gap: 0.4rem; }
.auth .details p { margin: 0; }
</style>
