<script setup lang="ts">
// Settings → Installed apps → one instance. The per-app management page: header
// (logo, name, description), the action row (Open / Stop·Start / Uninstall),
// and the logs at the bottom. Rendered inside the Settings shell as a nested
// route (/settings/apps/:id), so the left nav stays put.
//
// Two data sources: GET /apps/{id} for the live instance (state, scope, url),
// and GET /catalog/{manifest_id} for the logo + description. The catalog lookup
// is best-effort — a Door-2 (custom) app has no catalog entry, so it falls back
// to the generic glyph and no description.
import { computed, ref, watch } from "vue";
import { useRoute, useRouter, RouterLink } from "vue-router";
import { useQuery, useMutation, useQueryClient } from "@tanstack/vue-query";
import { AppWindow, ChevronDown } from "lucide-vue-next";
import { SwitchRoot, SwitchThumb } from "reka-ui";
import { api, waitForJob, type Instance, type CatalogDetail, type Job, type MailProviderOption, type AppSecrets, type AppSecret, type AppConfig, type AppConfigField } from "@/api";
import { useAuth } from "@/auth";
import AppLogs from "@/components/AppLogs.vue";
import Heading from "@/components/ui/Heading.vue";

const route = useRoute();
const router = useRouter();
const qc = useQueryClient();
const { currentUser } = useAuth();

const id = computed(() => String(route.params.id));

const appQuery = useQuery({
  queryKey: computed(() => ["apps", id.value]),
  queryFn: () => api.get<Instance>(`/apps/${id.value}`),
});
const app = computed(() => appQuery.data.value ?? null);

// Catalog lookup for logo + description. Best-effort: disabled until we know the
// manifest id, no retry (a Door-2 app legitimately 404s here), and any failure
// just leaves the glyph + "No description" fallback in place.
const catalogQuery = useQuery({
  queryKey: computed(() => ["catalog", app.value?.manifest_id]),
  queryFn: () => api.get<CatalogDetail>(`/catalog/${app.value!.manifest_id}`),
  enabled: computed(() => !!app.value?.manifest_id),
  retry: false,
});
const detail = computed(() => catalogQuery.data.value ?? null);

const brokenIcon = ref(false);
watch(id, () => { brokenIcon.value = false; });

// Authorization mirrors the brain: admins control any app; a member controls
// (and reads logs of) only their own personal instance. The list is already
// server-scoped, so a member never lands here for an app they can't see.
const canControl = computed(
  () => currentUser.value?.role === "admin" || app.value?.scope === "personal",
);

const running = computed(() => app.value?.state === "running");
const stopped = computed(() => app.value?.state === "stopped");
// `failed` shares the Start control as a click-to-retry (#154): the brain runs
// the identical Start transaction from `stopped` or `failed`. The logs below are
// the failure reason, so a retry from here isn't blind.
const failed = computed(() => app.value?.state === "failed");

function invalidate() {
  qc.invalidateQueries({ queryKey: ["apps"] });
  qc.invalidateQueries({ queryKey: ["apps", id.value] });
}

// awaitJob polls to terminal and throws on a failed job, so a job-level failure
// (e.g. compose up never goes healthy) surfaces via useMutation's isError —
// api.post only throws on the synchronous 4xx, not on a job that fails later.
async function awaitJob(job: Job): Promise<Job> {
  const done = await waitForJob(job.job_id);
  if (done.status === "failed") throw new Error(done.error?.message || "the operation failed");
  return done;
}

const stop = useMutation({
  mutationFn: async () => awaitJob(await api.post<Job>(`/apps/${id.value}/stop`)),
  onSettled: invalidate,
});

const start = useMutation({
  mutationFn: async () => awaitJob(await api.post<Job>(`/apps/${id.value}/start`)),
  onSettled: invalidate,
});

// Uninstall is destructive, so it's a two-step inline confirm rather than a bare
// button. On success we leave the now-dead detail page for the list.
const confirmingUninstall = ref(false);

// Logs start collapsed — they're a drill-down, not the first thing on the page.
const logsOpen = ref(false);
const uninstall = useMutation({
  mutationFn: async () => awaitJob(await api.del<Job>(`/apps/${id.value}`)),
  onSuccess: () => {
    qc.invalidateQueries({ queryKey: ["apps"] });
    router.push("/settings/apps");
  },
});

const busy = computed(
  () => stop.isPending.value || start.isPending.value || uninstall.isPending.value,
);

// ── Outgoing email (SERVICE_PROVISIONING.md # BYO outgoing mail) ─────────────
// Shown only for mail-capable apps (mail_supported comes from GET /apps/{id}).
// The options endpoint is id+label and readable by any signed-in user, so a
// member can rebind their own personal app. A rebind recreates the app's
// containers (env is read at container create), hence the job + hint below.
const mailOptions = useQuery({
  queryKey: ["mail-provider-options"],
  queryFn: () => api.get<{ providers: MailProviderOption[] }>("/mail-providers/options"),
  enabled: computed(() => !!app.value?.mail_supported && canControl.value),
});

const rebindMail = useMutation({
  mutationFn: async (providerId: string) =>
    awaitJob(await api.put<Job>(`/apps/${id.value}/mail-binding`, { provider_id: providerId })),
  onSettled: invalidate,
});

// ── Setup secrets (#152, SERVICE_PROVISIONING.md # Env-var injection) ─────────
// Owner-visible per-instance secrets a self-auth app declared `show: true` — the
// bootstrap credential the user reads to finish first sign-in. Gated to the same
// owner-or-admin rule as the controls (canControl); the brain re-checks. The
// section hides itself when the app declares none (the list comes back empty).
const secretsQuery = useQuery({
  queryKey: computed(() => ["app-secrets", id.value]),
  queryFn: () => api.get<AppSecrets>(`/apps/${id.value}/secrets`),
  enabled: computed(() => canControl.value),
});
const secrets = computed(() => secretsQuery.data.value?.secrets ?? []);

// Masked by default — revealed per-secret on demand so the value isn't shoulder-
// surfaced just by opening the page. A reassigned Set keeps the template reactive.
const revealed = ref(new Set<string>());
function toggleReveal(name: string) {
  const next = new Set(revealed.value);
  if (next.has(name)) next.delete(name);
  else next.add(name);
  revealed.value = next;
}

// Copy is best-effort: navigator.clipboard is unavailable on the HTTP-only
// .local origin, so the value stays select-all on screen as the fallback.
const copied = ref<string | null>(null);
async function copySecret(s: AppSecret) {
  try {
    await navigator.clipboard.writeText(s.value);
    copied.value = s.name;
    setTimeout(() => {
      if (copied.value === s.name) copied.value = null;
    }, 1500);
  } catch {
    // No clipboard on an insecure context — the value is on screen to copy by hand.
  }
}

// ── Settings — user-supplied config (APP_MANIFEST.md # D4) ───────────────────
// Fields the app declared a `config:` block for (an API token, a model picker).
// GET never returns a secret's value (only `set`), so secrets show as set/not-set
// with a Replace affordance; non-secret values are editable inline. Save sends a
// PARTIAL update — only the fields the user actually changed — so an untouched
// secret is never resent (we don't have it) and never accidentally cleared. The
// PUT restarts the app as a job. Gated to canControl; the brain re-checks.
const configQuery = useQuery({
  queryKey: computed(() => ["app-config", id.value]),
  queryFn: () => api.get<AppConfig>(`/apps/${id.value}/config`),
  enabled: computed(() => canControl.value),
});
const configFields = computed<AppConfigField[]>(() => configQuery.data.value?.fields ?? []);

// edits is the local buffer: non-secret fields start at their stored value;
// secret fields start empty and only carry a value once the user hits Replace.
// replacing tracks which secrets are mid-edit (showing an input vs the set badge).
const edits = ref<Record<string, string>>({});
const replacing = ref<Set<string>>(new Set());

watch(
  configFields,
  (fields) => {
    const next: Record<string, string> = {};
    for (const f of fields) next[f.app_env] = f.secret ? "" : f.value;
    edits.value = next;
    replacing.value = new Set();
  },
  { immediate: true },
);

function startReplace(appEnv: string) {
  const next = new Set(replacing.value);
  next.add(appEnv);
  replacing.value = next;
  edits.value[appEnv] = "";
}
function cancelReplace(appEnv: string) {
  const next = new Set(replacing.value);
  next.delete(appEnv);
  replacing.value = next;
  edits.value[appEnv] = "";
}

// changedFields is the partial-update payload: a non-secret field whose buffer
// differs from its stored value, and a secret only when the user typed a new,
// non-empty value (a blank Replace box is ignored — we never blank a secret here).
function changedFields(): Record<string, string> {
  const out: Record<string, string> = {};
  for (const f of configFields.value) {
    const v = edits.value[f.app_env] ?? "";
    if (f.secret) {
      if (replacing.value.has(f.app_env) && v !== "") out[f.app_env] = v;
    } else if (v !== f.value) {
      out[f.app_env] = v;
    }
  }
  return out;
}
const dirty = computed(() => Object.keys(changedFields()).length > 0);

// Block Save if a required NON-secret field has been cleared — the brain would
// 422 it. A required secret is already set (install enforced it) and can only be
// replaced, never blanked from here, so it never gates Save.
const configValid = computed(() =>
  configFields.value.every(
    (f) => !f.required || f.secret || (edits.value[f.app_env] ?? "").trim() !== "",
  ),
);

const saveConfig = useMutation({
  mutationFn: async () => awaitJob(await api.put<Job>(`/apps/${id.value}/config`, { fields: changedFields() })),
  onSettled: () => {
    invalidate();
    qc.invalidateQueries({ queryKey: ["app-config", id.value] });
  },
});
</script>

<template>
  <div class="flex h-full flex-col gap-8 pt-2">
    <RouterLink to="/settings/apps" class="inline-block text-sm text-muted-foreground hover:text-foreground">
      ← Installed apps
    </RouterLink>

    <p v-if="appQuery.isLoading.value" class="text-sm text-muted-foreground">Loading…</p>
    <p v-else-if="appQuery.isError.value" class="text-sm text-destructive">
      Couldn't load this app. {{ (appQuery.error.value as Error)?.message }}
    </p>

    <template v-else-if="app">
      <!-- Header: logo · name/description · state -->
      <header class="flex flex-col gap-5 sm:flex-row sm:items-center">
        <div
          class="grid size-20 shrink-0 place-items-center overflow-hidden rounded-3xl border border-border bg-card text-muted-foreground"
        >
          <img
            v-if="detail?.icon_url && !brokenIcon"
            :src="detail.icon_url"
            :alt="`${app.name} icon`"
            class="size-full object-cover"
            @error="brokenIcon = true"
          />
          <AppWindow v-else class="size-9" />
        </div>

        <div class="min-w-0 flex-1">
          <Heading :level="2">{{ app.name }}</Heading>
          <p v-if="detail?.short_description" class="mt-0.5 text-sm text-muted-foreground">
            {{ detail.short_description }}
          </p>
          <p class="mt-1 text-xs uppercase tracking-wide text-muted-foreground">{{ app.state }}</p>
        </div>
      </header>

      <!-- Action row -->
      <section class="flex flex-wrap items-center gap-2">
        <a
          v-if="running"
          :href="app.url"
          target="_blank"
          rel="noopener"
          class="rounded-lg bg-accent px-4 py-1.5 text-sm font-medium text-accent-foreground hover:opacity-90"
        >
          Open
        </a>

        <button
          v-if="canControl && running"
          class="rounded-lg border border-border px-3 py-1.5 text-sm hover:bg-muted disabled:opacity-50"
          :disabled="busy"
          @click="stop.mutate()"
        >
          {{ stop.isPending.value ? "Stopping…" : "Stop service" }}
        </button>
        <button
          v-else-if="canControl && (stopped || failed)"
          class="rounded-lg border border-border px-3 py-1.5 text-sm hover:bg-muted disabled:opacity-50"
          :disabled="busy"
          @click="start.mutate()"
        >
          <template v-if="failed">{{ start.isPending.value ? "Retrying…" : "Retry" }}</template>
          <template v-else>{{ start.isPending.value ? "Starting…" : "Start service" }}</template>
        </button>

        <template v-if="canControl">
          <template v-if="confirmingUninstall">
            <span class="text-sm text-muted-foreground">Uninstall {{ app.name }}? This deletes its data.</span>
            <button
              class="rounded-lg border border-destructive/40 bg-destructive/10 px-3 py-1.5 text-sm text-destructive hover:bg-destructive/20 disabled:opacity-50"
              :disabled="busy"
              @click="uninstall.mutate()"
            >
              {{ uninstall.isPending.value ? "Uninstalling…" : "Confirm uninstall" }}
            </button>
            <button
              class="rounded-lg border border-border px-3 py-1.5 text-sm hover:bg-muted"
              :disabled="busy"
              @click="confirmingUninstall = false"
            >
              Cancel
            </button>
          </template>
          <button
            v-else
            class="rounded-lg border border-border px-3 py-1.5 text-sm hover:bg-muted disabled:opacity-50"
            :disabled="busy"
            @click="confirmingUninstall = true"
          >
            Uninstall
          </button>
        </template>
      </section>

      <!-- Action error surface (job failure / 409 / host 5xx) -->
      <p v-if="stop.isError.value" class="text-sm text-destructive">
        Couldn't stop: {{ (stop.error.value as Error)?.message }}
      </p>
      <p v-if="start.isError.value" class="text-sm text-destructive">
        Couldn't start: {{ (start.error.value as Error)?.message }}
      </p>
      <p v-if="uninstall.isError.value" class="text-sm text-destructive">
        Couldn't uninstall: {{ (uninstall.error.value as Error)?.message }}
      </p>

      <!-- Outgoing email — provider binding for mail-capable apps. -->
      <section v-if="app.mail_supported && canControl" class="space-y-3">
        <Heading :level="3">Outgoing email</Heading>
        <div class="flex flex-wrap items-center gap-3 rounded-2xl border border-border bg-card p-5">
          <div class="min-w-0 flex-1">
            <div class="text-sm font-medium">Send email as</div>
            <div class="text-xs text-muted-foreground">
              {{ rebindMail.isPending.value ? "Applying — the app restarts briefly." : "Changing this restarts the app briefly." }}
            </div>
          </div>
          <select
            :value="app.mail_provider_id ?? ''"
            class="rounded-lg border border-border bg-background px-2 py-1 text-sm outline-none focus:border-accent disabled:opacity-50"
            :disabled="rebindMail.isPending.value || busy"
            @change="(e) => rebindMail.mutate((e.target as HTMLSelectElement).value)"
          >
            <option value="">None — email features off</option>
            <option v-for="p in (mailOptions.data.value?.providers ?? [])" :key="p.id" :value="p.id">
              {{ p.label }}
            </option>
          </select>
        </div>
        <p v-if="rebindMail.isError.value" class="text-sm text-destructive">
          Couldn't change the email account: {{ (rebindMail.error.value as Error)?.message }}
        </p>
      </section>

      <!-- Setup secrets — owner-visible bootstrap credentials for self-auth apps.
           Shown only when the app declared one (`show: true`); masked until the
           owner reveals it. -->
      <p v-if="secretsQuery.isError.value" class="text-sm text-destructive">
        Couldn't load secrets: {{ (secretsQuery.error.value as Error)?.message }}
      </p>
      <section v-if="canControl && secrets.length" class="space-y-3">
        <Heading :level="3">Setup secrets</Heading>
        <p class="text-xs text-muted-foreground">
          Use these to finish signing in to {{ app.name }} the first time. Keep them private.
        </p>
        <ul class="space-y-2">
          <li
            v-for="s in secrets"
            :key="s.name"
            class="flex flex-wrap items-center gap-3 rounded-2xl border border-border bg-card p-5"
          >
            <div class="min-w-0 flex-1">
              <div class="text-xs font-medium text-muted-foreground">{{ s.name }}</div>
              <div class="mt-0.5 break-all font-mono text-sm" :class="{ 'select-all': revealed.has(s.name) }">
                {{ revealed.has(s.name) ? s.value : "••••••••••••" }}
              </div>
            </div>
            <button
              type="button"
              class="rounded-lg border border-border px-3 py-1.5 text-sm hover:bg-muted"
              @click="toggleReveal(s.name)"
            >
              {{ revealed.has(s.name) ? "Hide" : "Reveal" }}
            </button>
            <button
              type="button"
              class="rounded-lg border border-border px-3 py-1.5 text-sm hover:bg-muted"
              @click="copySecret(s)"
            >
              {{ copied === s.name ? "Copied" : "Copy" }}
            </button>
          </li>
        </ul>
      </section>

      <!-- Settings — user-supplied config (APP_MANIFEST.md # D4). Hidden when the
           app declares no config: block. Non-secret values edit inline; secrets
           show set/not-set with a Replace box. Save sends only what changed and
           restarts the app. -->
      <p v-if="configQuery.isError.value" class="text-sm text-destructive">
        Couldn't load settings: {{ (configQuery.error.value as Error)?.message }}
      </p>
      <section v-if="canControl && configFields.length" class="space-y-3">
        <Heading :level="3">Settings</Heading>
        <p class="text-xs text-muted-foreground">Changing these restarts {{ app.name }} briefly.</p>
        <div class="space-y-2">
          <div
            v-for="f in configFields"
            :key="f.app_env"
            class="space-y-2 rounded-2xl border border-border bg-card p-5"
          >
            <div>
              <div class="text-sm font-medium">
                {{ f.title }}<span v-if="f.required" class="text-destructive"> *</span>
              </div>
              <div class="text-xs text-muted-foreground">{{ f.description }}</div>
              <div class="mt-0.5 font-mono text-xs text-muted-foreground">Sets {{ f.app_env }}</div>
            </div>

            <!-- secret: set/not-set badge + replace affordance -->
            <template v-if="f.secret">
              <div v-if="!replacing.has(f.app_env)" class="flex items-center gap-3">
                <span class="text-sm text-muted-foreground">{{ f.set ? "•••••••• (set)" : "Not set" }}</span>
                <button
                  type="button"
                  class="rounded-lg border border-border px-3 py-1 text-sm hover:bg-muted"
                  @click="startReplace(f.app_env)"
                >
                  {{ f.set ? "Replace" : "Set" }}
                </button>
              </div>
              <div v-else class="flex items-center gap-2">
                <input
                  v-model="edits[f.app_env]"
                  type="password"
                  autocomplete="off"
                  placeholder="Enter a new value"
                  class="w-full rounded-lg border border-border bg-background px-3 py-1.5 text-sm outline-none focus:border-accent"
                />
                <button
                  type="button"
                  class="shrink-0 rounded-lg border border-border px-3 py-1.5 text-sm hover:bg-muted"
                  @click="cancelReplace(f.app_env)"
                >
                  Cancel
                </button>
              </div>
            </template>

            <!-- non-secret enum: select of declared options -->
            <select
              v-else-if="f.type === 'enum'"
              v-model="edits[f.app_env]"
              class="w-full rounded-lg border border-border bg-background px-3 py-1.5 text-sm outline-none focus:border-accent"
            >
              <option v-if="!f.required" value="">None</option>
              <option v-for="opt in (f.options ?? [])" :key="opt" :value="opt">{{ opt }}</option>
            </select>

            <!-- non-secret bool: toggle; value travels as "true"/"false" -->
            <SwitchRoot
              v-else-if="f.type === 'bool'"
              :model-value="edits[f.app_env] === 'true'"
              class="relative inline-flex h-5 w-9 shrink-0 cursor-pointer items-center rounded-full border border-border bg-muted outline-none transition-colors data-[state=checked]:border-accent data-[state=checked]:bg-accent"
              @update:model-value="(on: boolean) => (edits[f.app_env] = on ? 'true' : 'false')"
            >
              <SwitchThumb
                class="pointer-events-none block size-4 translate-x-0.5 rounded-full bg-card shadow transition-transform data-[state=checked]:translate-x-[1.125rem]"
              />
            </SwitchRoot>

            <!-- non-secret text -->
            <input
              v-else
              v-model="edits[f.app_env]"
              type="text"
              class="w-full rounded-lg border border-border bg-background px-3 py-1.5 text-sm outline-none focus:border-accent"
            />
          </div>
        </div>

        <div class="flex items-center gap-3">
          <button
            type="button"
            class="rounded-lg bg-accent px-4 py-1.5 text-sm font-medium text-accent-foreground hover:opacity-90 disabled:opacity-50"
            :disabled="!dirty || !configValid || saveConfig.isPending.value"
            @click="saveConfig.mutate()"
          >
            {{ saveConfig.isPending.value ? "Saving…" : "Save changes" }}
          </button>
          <span v-if="saveConfig.isPending.value" class="text-xs text-muted-foreground">
            The app restarts briefly.
          </span>
        </div>
        <p v-if="saveConfig.isError.value" class="text-sm text-destructive">
          Couldn't save settings: {{ (saveConfig.error.value as Error)?.message }}
        </p>
      </section>

      <!-- Logs — collapsed by default; a full-width accordion row (styled like
           the Installed apps list rows) that expands the log panel on click. The
           chevron at the end rotates to signal expansion. -->
      <section
        v-if="canControl"
        class="flex flex-col gap-2"
        :class="{ 'min-h-0 flex-1': logsOpen }"
      >
        <button
          type="button"
          class="flex w-full shrink-0 items-center justify-between gap-3 rounded-2xl border border-border bg-card p-5 text-sm hover:bg-muted"
          :aria-expanded="logsOpen"
          @click="logsOpen = !logsOpen"
        >
          <span class="font-medium">Logs</span>
          <ChevronDown class="size-4 shrink-0 text-muted-foreground transition-transform" :class="{ 'rotate-180': logsOpen }" />
        </button>
        <AppLogs v-if="logsOpen" :id="app.id" fill class="min-h-0 flex-1" />
      </section>
    </template>
  </div>
</template>
