<script setup lang="ts">
// First-run Step 3 — time zone (FIRST_RUN.md). The spec's auto-detect is
// realized here from the BROWSER (Intl.DateTimeFormat), not IP geolocation: it
// needs no backend geo-IP dependency, works offline, and the setup device shares
// the box's locale in the "old laptop on the LAN" case. A full IANA list is the
// manual fallback. The choice is sent to the brain's timedatectl seam; it is
// always overridable later from Settings.
import { ref } from "vue";
import { api, type ApiError } from "../api";

const emit = defineEmits<{ next: [] }>();

const detected = Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC";
// Intl.supportedValuesOf is the canonical zone list (all modern browsers); fall
// back to the detected zone alone if the runtime lacks it.
const zones: string[] =
  typeof Intl.supportedValuesOf === "function"
    ? Intl.supportedValuesOf("timeZone")
    : [detected];
if (!zones.includes(detected)) zones.unshift(detected);

const selected = ref(detected);
const submitting = ref(false);
const error = ref("");

async function submit() {
  error.value = "";
  submitting.value = true;
  try {
    await api.post("/system/timezone", { timezone: selected.value });
    emit("next");
  } catch (e) {
    error.value = (e as ApiError).message || "Could not set the time zone";
  } finally {
    submitting.value = false;
  }
}
</script>

<template>
  <form class="card" @submit.prevent="submit">
    <h2>Time zone</h2>
    <p class="hint">Detected from this device. Change it if it's wrong — you can adjust it later in Settings.</p>
    <label>
      Time zone
      <select v-model="selected">
        <option v-for="z in zones" :key="z" :value="z">{{ z }}</option>
      </select>
    </label>
    <button type="submit" :disabled="submitting">
      {{ submitting ? "Saving…" : "Continue" }}
    </button>
    <p v-if="error" class="error">{{ error }}</p>
  </form>
</template>

<style>
.auth select { border: 1px solid #ddd; border-radius: 8px; padding: 0.5rem 0.7rem; font-size: 0.95rem; background: #fff; }
</style>
