<script setup lang="ts">
// Door-2 custom-container install (DASHBOARD.md # Door-2 custom container install
// flow). A dedicated full-screen form — distinct from the catalog consent dialog
// — that authors a synthetic manifest from a pasted docker-compose.yml. Admin-only.
//
// Flow: paste/upload compose → POST /apps/custom/inspect (read-only) drives the
// service dropdown + best-effort main-port prefill from `expose:` → submit to
// POST /apps/custom. Admission runs on submit; its field-named 422 is coached
// inline against the compose rather than thrown as an opaque toast. Scope follows
// the store row's split-button convention (silent personal on a single-user box).
import { ref, computed, watch } from "vue";
import { useRouter } from "vue-router";
import { useMutation, useQueryClient } from "@tanstack/vue-query";
import { SwitchRoot, SwitchThumb } from "reka-ui";
import { useAuth } from "../auth";
import { api, waitForJob, ApiError, type Job, type Scope, type CustomInspectResult } from "../api";
import SplitButton from "../components/SplitButton.vue";

const router = useRouter();
const qc = useQueryClient();
const { currentUser, singleUserMode } = useAuth();

// Door 2 is admin-only. The Store hides the affordance from members, but guard
// the route directly too (deep-link, or a role change mid-session).
const isAdmin = computed(() => currentUser.value?.role === "admin");
watch(
  currentUser,
  (u) => {
    if (u && u.role !== "admin") router.replace("/store");
  },
  { immediate: true },
);

// ── form state ────────────────────────────────────────────────────────────────
const compose = ref("");
const name = ref("");
const services = ref<string[]>([]);
const mainService = ref("");
const mainPort = ref<number | undefined>(undefined);
// Once the admin edits the port, inspect stops overwriting their value.
const portTouched = ref(false);
const internet = ref(true);
// 422 synthesize/admission rejection (field-named), shown inline against the compose.
const submitError = ref<string | null>(null);

const slug = computed(() =>
  name.value
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-+|-+$/g, ""),
);

// ── inspect (advisory, debounced) ───────────────────────────────────────────────
let inspectTimer: ReturnType<typeof setTimeout> | undefined;

async function runInspect() {
  if (compose.value.trim() === "") {
    services.value = [];
    return;
  }
  try {
    const res = await api.post<CustomInspectResult>("/apps/custom/inspect", {
      compose: compose.value,
      main_service: mainService.value || undefined,
    });
    services.value = res.services;
    if (res.services.length === 1) {
      mainService.value = res.services[0] ?? "";
    } else if (!res.services.includes(mainService.value)) {
      mainService.value = "";
    }
    if (!portTouched.value && res.main_port > 0) {
      mainPort.value = res.main_port;
    }
  } catch {
    // Inspect is advisory: an incomplete or not-yet-valid paste just leaves the
    // dropdown empty and the port asked. Real errors surface on submit.
    services.value = [];
  }
}

watch(compose, () => {
  clearTimeout(inspectTimer);
  inspectTimer = setTimeout(runInspect, 350);
});

// Picking a service re-infers the port from that service's `expose:`.
function onServiceChange() {
  if (!portTouched.value) runInspect();
}

function onFile(e: Event) {
  const file = (e.target as HTMLInputElement).files?.[0];
  if (!file) return;
  file.text().then((t) => {
    compose.value = t;
  });
}

// ── install ─────────────────────────────────────────────────────────────────────
const install = useMutation({
  mutationFn: async (scope: Scope) => {
    submitError.value = null;
    const job = await api.post<Job>("/apps/custom", {
      name: name.value,
      compose: compose.value,
      main_service: mainService.value || undefined,
      main_port: Number(mainPort.value),
      internet: internet.value,
      scope,
    });
    const done = await waitForJob(job.job_id);
    if (done.status !== "completed") {
      throw new Error(done.error?.message ?? "The install didn't finish.");
    }
    return done;
  },
  onSuccess: async () => {
    await qc.invalidateQueries({ queryKey: ["apps"] });
    router.push("/store");
  },
  onError: (err: unknown) => {
    // A 422 carries the brain's field-named synthesize/admission message — coach
    // it inline against the compose, never an opaque toast.
    submitError.value = err instanceof ApiError ? err.message : (err as Error).message;
  },
});

const canSubmit = computed(
  () =>
    compose.value.trim() !== "" &&
    name.value.trim() !== "" &&
    // A multi-service compose forces a service choice; one-or-unknown is fine —
    // the brain infers/validates the rest.
    (services.value.length <= 1 || mainService.value !== "") &&
    Number(mainPort.value) > 0 &&
    !install.isPending.value,
);

// Scope mirrors the store row's split-button convention: the primary action is
// personal; an admin on a multi-user box gets a dropdown to install for the whole
// household. Single-user boxes silently install personal (# Single-user).
const scopeItems = computed(() =>
  isAdmin.value && !singleUserMode.value
    ? [{ label: "Install for the whole household", action: () => install.mutate("household") }]
    : [],
);

const submitLabel = computed(() => (install.isPending.value ? "Installing…" : "Install"));
</script>

<template>
  <div v-if="isAdmin" class="mx-auto max-w-2xl space-y-6 pt-2">
    <header class="space-y-1">
      <RouterLink to="/store" class="text-xs text-muted-foreground hover:underline">← Back to Store</RouterLink>
      <h1 class="text-lg font-semibold">Install a custom container</h1>
      <p class="text-sm text-muted-foreground">
        Paste a <code>docker-compose.yml</code> to run an app that isn't in the catalog. It installs into the same
        sandbox as a store app.
      </p>
    </header>

    <!-- 1. Compose: paste or upload -->
    <section class="space-y-2">
      <label class="text-sm font-medium" for="compose">Compose file</label>
      <textarea
        id="compose"
        v-model="compose"
        rows="10"
        spellcheck="false"
        placeholder="Paste a docker-compose.yml…"
        class="w-full rounded-lg border border-border px-3 py-2 font-mono text-xs"
      />
      <div class="flex items-center gap-2 text-xs text-muted-foreground">
        <span>or</span>
        <input type="file" accept=".yml,.yaml,text/yaml,text/plain" class="text-xs" @change="onFile" />
      </div>
      <!-- 422 synthesize/admission coaching, inline against the offending compose -->
      <p
        v-if="submitError"
        class="rounded-lg border border-destructive/40 bg-destructive/10 px-3 py-2 text-sm text-destructive"
      >
        {{ submitError }}
      </p>
    </section>

    <!-- 2. App name + live URL preview -->
    <section class="space-y-2">
      <label class="text-sm font-medium" for="name">App name</label>
      <input
        id="name"
        v-model="name"
        placeholder="My App"
        class="w-full rounded-lg border border-border px-3 py-2 text-sm"
      />
      <p class="text-xs text-muted-foreground">
        It'll be reachable at <code>{{ slug || "your-app" }}.local</code>
      </p>
    </section>

    <!-- 3. Main service — auto when one, required dropdown when several -->
    <section v-if="services.length > 1" class="space-y-2">
      <label class="text-sm font-medium" for="main-service">Main service</label>
      <select
        id="main-service"
        v-model="mainService"
        class="w-full rounded-lg border border-border px-3 py-2 text-sm"
        @change="onServiceChange"
      >
        <option value="" disabled>Choose the service the dashboard opens…</option>
        <option v-for="s in services" :key="s" :value="s">{{ s }}</option>
      </select>
      <p class="text-xs text-muted-foreground">This compose has several services — pick the one users open.</p>
    </section>
    <p v-else-if="services.length === 1" class="text-xs text-muted-foreground">
      Main service: <code>{{ services[0] }}</code> (auto-detected)
    </p>

    <!-- 4. Main port -->
    <section class="space-y-2">
      <label class="text-sm font-medium" for="main-port">Main port</label>
      <input
        id="main-port"
        v-model.number="mainPort"
        type="number"
        min="1"
        max="65535"
        placeholder="e.g. 8080"
        class="w-40 rounded-lg border border-border px-3 py-2 text-sm"
        @input="portTouched = true"
      />
      <p class="text-xs text-muted-foreground">
        The port your app listens on <em>inside</em> the container — check the image's docs. We prefill it from the
        compose when we can; always double-check.
      </p>
    </section>

    <!-- 5. Internet access -->
    <section class="flex items-start justify-between gap-4">
      <div class="min-w-0">
        <div class="text-sm font-medium">Internet access</div>
        <div class="text-xs text-muted-foreground">Let this app reach the internet. On by default for custom apps.</div>
      </div>
      <SwitchRoot
        v-model="internet"
        aria-label="Internet access"
        class="relative inline-flex h-5 w-9 shrink-0 cursor-pointer items-center rounded-full border border-border bg-muted outline-none transition-colors data-[state=checked]:border-accent data-[state=checked]:bg-accent"
      >
        <SwitchThumb
          class="pointer-events-none block size-4 translate-x-0.5 rounded-full bg-card shadow transition-transform data-[state=checked]:translate-x-[1.125rem]"
        />
      </SwitchRoot>
    </section>

    <!-- TOFU / no-auto-update honesty note -->
    <p class="rounded-lg border border-border bg-muted/40 px-3 py-2 text-xs text-muted-foreground">
      malmo pins the <strong>exact image it pulls now</strong> and won't change it underneath you — a custom app
      <strong>does not auto-update</strong>. To move to a newer image, uninstall and paste again.
    </p>

    <!-- 6. Scope + submit (store split-button convention) -->
    <div class="flex items-center gap-3 border-t border-border pt-4">
      <SplitButton
        :label="submitLabel"
        :disabled="!canSubmit"
        :items="scopeItems"
        @click="install.mutate('personal')"
      />
      <RouterLink to="/store" class="text-sm text-muted-foreground hover:underline">Cancel</RouterLink>
    </div>
  </div>
</template>
