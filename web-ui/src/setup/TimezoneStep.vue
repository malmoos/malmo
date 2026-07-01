<script setup lang="ts">
// First-run wizard, step 2 — system time zone (FIRST_RUN.md # Step 3, TIME.md).
// FIRST_RUN specs IP-geolocation auto-detect; we default to the browser's
// resolved zone instead — no server-side geolocation dependency, and for a
// hosted box configured "from my own laptop" the operator's browser zone is the
// better signal. The full IANA list is always shown so the default is
// overridable here (not only later in Settings).
import { ref } from "vue";
import { setSystemTimezone } from "../auth";
import type { ApiError } from "../api";
import Button from "@/components/ui/Button.vue";

const emit = defineEmits<{ done: [] }>();

const detected = Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC";
// Intl.supportedValuesOf ships the browser's IANA list — no bundled tz table.
// Guard for engines that predate it (fall back to just the detected zone).
const zones: string[] =
  typeof Intl.supportedValuesOf === "function"
    ? Intl.supportedValuesOf("timeZone")
    : [detected];
const zone = ref(detected);
const submitting = ref(false);
const error = ref("");

async function submit() {
  error.value = "";
  submitting.value = true;
  try {
    await setSystemTimezone(zone.value);
    emit("done");
  } catch (e) {
    error.value = (e as ApiError).message || "Couldn't set the time zone";
  } finally {
    submitting.value = false;
  }
}
</script>

<template>
  <form @submit.prevent="submit">
    <h2>Time zone</h2>
    <p class="hint">Used for schedules, logs, and timestamps across the box.</p>
    <label>
      Time zone
      <select v-model="zone" required>
        <option v-for="z in zones" :key="z" :value="z">{{ z }}</option>
      </select>
    </label>
    <Button type="submit" :disabled="submitting" class="mt-2">
      {{ submitting ? "Saving…" : "Continue" }}
    </Button>
    <p v-if="error" class="error">{{ error }}</p>
  </form>
</template>
