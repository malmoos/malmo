<script setup lang="ts">
// Settings → Outgoing email — admin-only SMTP provider management
// (SERVICE_PROVISIONING.md # BYO outgoing mail, issues #122 and #426). Apps
// that can send email bind to one of these providers at install time (or later
// from their detail page); the brain injects the credentials as MALMO_MAIL_*
// env vars.
//
// Adding an account is two steps: pick a provider from the grid, then fill in
// only what the preset cannot know — the credential, the from address, a
// username when the provider does not fix one, and a region for the two
// providers whose region changes the host. Host, port and encryption are
// prefilled and live behind an Advanced disclosure, so a non-standard endpoint
// is never trapped. "Custom SMTP server" is the old seven-field form: the same
// disclosure, open, with nothing prefilled.
//
// Mirrors UsersSection: admin redirect as defence in depth, every mutation
// wrapped in withElevation, guard rejections surface as inline errors. The
// test-send is the one non-elevated action (it changes nothing).
import { ref, computed, watch } from "vue";
import { useRouter } from "vue-router";
import { useQuery, useMutation, useQueryClient } from "@tanstack/vue-query";
import { api, ApiError, type MailProvider, type MailPreset } from "@/api";
import { withElevation } from "@/elevate";
import { useAuth, isHosted } from "@/auth";
import Button from "@/components/ui/Button.vue";
import MailProviderLogo from "@/components/MailProviderLogo.vue";

const router = useRouter();
const qc = useQueryClient();
const { currentUser } = useAuth();

// Admin-only — redirect members immediately (mirrors UsersSection).
watch(
  currentUser,
  (u) => {
    if (u && u.role !== "admin") router.replace("/settings");
  },
  { immediate: true },
);

// ── provider list + presets ─────────────────────────────────────────────────────
const providers = useQuery({
  queryKey: ["mail-providers"],
  queryFn: () => api.get<{ providers: MailProvider[] }>("/mail-providers"),
});

// The preset table is static server-side data, so it never needs refetching
// within a session.
const presets = useQuery({
  queryKey: ["mail-presets"],
  queryFn: () => api.get<{ presets: MailPreset[] }>("/mail-presets"),
  staleTime: Infinity,
});

const presetList = computed(() => presets.data.value?.presets ?? []);
function presetById(id: string): MailPreset | undefined {
  return presetList.value.find((p) => p.id === id);
}

// ── shared form shape (create + edit) ───────────────────────────────────────────
type ProviderForm = {
  label: string;
  host: string;
  port: number;
  username: string;
  password: string;
  from_address: string;
  encryption: "none" | "starttls" | "tls";
  provider_type: string;
  region: string;
};

function emptyForm(): ProviderForm {
  return {
    label: "", host: "", port: 587, username: "", password: "",
    from_address: "", encryption: "starttls", provider_type: "custom", region: "",
  };
}

// formFromPreset seeds a blank form from the preset the admin picked. The label
// defaults to the provider name so it stops being a field they must invent; a
// duplicate surfaces the existing 409.
function formFromPreset(p: MailPreset): ProviderForm {
  const f = emptyForm();
  f.provider_type = p.id;
  f.label = p.id === "custom" ? "" : p.label;
  f.port = p.port;
  f.encryption = p.encryption as ProviderForm["encryption"];
  f.region = p.region?.default ?? "";
  f.host = hostFor(p, f.region);
  if (p.username_mode === "fixed") f.username = p.username_fixed;
  else if (p.username_mode === "user") f.username = p.username_prefill ?? "";
  return f;
}

// hostFor resolves the SMTP host for a preset: the static one, or the host the
// chosen region option names (SES and Mailgun are the only two, and Mailgun's
// EU host is a prefix rather than a region code — hence a host per option).
function hostFor(p: MailPreset, region: string): string {
  if (!p.region) return p.host;
  const opts = p.region.options ?? [];
  const opt = opts.find((o) => o.value === region) ?? opts[0];
  return opt?.host ?? "";
}

// Postmark wants the same server token as username and password, so the form
// keeps the two in step and shows one field.
//
// A blank password is never copied across. On edit it means "keep the stored
// credential", so the paired username has to be kept too — copying the empty
// value over it would wipe the stored username while leaving the password
// intact, and the account would fail AUTH on its next send with nothing in the
// UI to say why.
function syncSameAsPassword(f: ProviderForm, p: MailPreset | undefined) {
  if (p?.username_mode === "same_as_password" && f.password !== "") f.username = f.password;
}

function onRegionChange(f: ProviderForm) {
  const p = presetById(f.provider_type);
  if (p) f.host = hostFor(p, f.region);
}

function formValid(f: ProviderForm): boolean {
  return !!f.label.trim() && !!f.host.trim() && f.port >= 1 && f.port <= 65535 && f.from_address.includes("@");
}

// A hosted box reaches 587 and 2525 only — 25 and 465 get no SYN-ACK, so a
// provider saved on implicit TLS never delivers (SERVICE_PROVISIONING.md # BYO
// outgoing mail). Warn rather than forbid: an appliance on a home LAN reaches
// 465 fine, and some providers prefer it.
function portWarning(f: ProviderForm): string {
  if (!isHosted()) return "";
  if (f.port !== 25 && f.port !== 465) return "";
  return `This box cannot reach port ${f.port}. Use port 587 with STARTTLS — every provider in the list supports it.`;
}

function errorMessage(e: unknown): string {
  // A dismissed elevation prompt is a deliberate no-op, not a failure.
  if (e instanceof ApiError && e.code === "elevation_cancelled") return "";
  return e instanceof ApiError ? e.message : "Something went wrong.";
}

// ── create ───────────────────────────────────────────────────────────────────────
// pickedPreset is null while the grid is showing; picking one opens the form.
const pickedPreset = ref<MailPreset | null>(null);
const newForm = ref<ProviderForm>(emptyForm());
const newAdvancedOpen = ref(false);
const createError = ref<string | null>(null);

function pickPreset(p: MailPreset) {
  pickedPreset.value = p;
  newForm.value = formFromPreset(p);
  // Custom is the old form: no preset values to hide, so open the disclosure.
  newAdvancedOpen.value = p.id === "custom";
  createError.value = null;
}

function cancelPick() {
  pickedPreset.value = null;
  newForm.value = emptyForm();
  createError.value = null;
}

const create = useMutation({
  mutationFn: () => {
    syncSameAsPassword(newForm.value, pickedPreset.value ?? undefined);
    return withElevation(() => api.post<MailProvider>("/mail-providers", bodyOf(newForm.value)));
  },
  onSuccess: () => {
    cancelPick();
    qc.invalidateQueries({ queryKey: ["mail-providers"] });
  },
  onError: (e) => {
    createError.value = errorMessage(e);
  },
});

// bodyOf drops the UI-only region field: the brain stores the resolved host, not
// the region that produced it (the server never re-derives host from a preset,
// so an advanced override survives — DECISIONS.md 2026-08-27 D3).
function bodyOf(f: ProviderForm) {
  const { region: _region, ...body } = f;
  return body;
}

// ── per-row state ────────────────────────────────────────────────────────────────
// Only one expanded panel (edit / test / delete-confirm) per row at a time.
const editFor = ref<string | null>(null);
const editForm = ref<ProviderForm>(emptyForm());
const editAdvancedOpen = ref(false);
const testFor = ref<string | null>(null);
const testTo = ref("");
const testSent = ref<Record<string, string>>({}); // id → "sent to <addr>" confirmation
const confirmDeleteFor = ref<string | null>(null);
const rowError = ref<Record<string, string>>({});

const editPreset = computed(() => presetById(editForm.value.provider_type));

function clearRowError(id: string) {
  const next = { ...rowError.value };
  delete next[id];
  rowError.value = next;
}

function setRowError(id: string, e: unknown) {
  rowError.value = { ...rowError.value, [id]: errorMessage(e) };
}

function startEdit(p: MailProvider) {
  testFor.value = null;
  confirmDeleteFor.value = null;
  editFor.value = editFor.value === p.id ? null : p.id;
  // Password stays blank — an empty password on save keeps the stored one.
  editForm.value = {
    label: p.label, host: p.host, port: p.port, username: p.username,
    password: "", from_address: p.from_address,
    encryption: p.encryption as ProviderForm["encryption"],
    provider_type: p.provider_type,
    // The region is not stored — only the host it resolved to — so edit shows
    // the host in the advanced fields rather than guessing the region back.
    region: "",
  };
  // A hand-typed provider (including every row that predates presets) opens on
  // the full form; a preset row opens on the short one.
  editAdvancedOpen.value = p.provider_type === "custom";
}

function startTest(id: string) {
  editFor.value = null;
  confirmDeleteFor.value = null;
  testFor.value = testFor.value === id ? null : id;
  testTo.value = "";
}

// ── update ───────────────────────────────────────────────────────────────────────
const update = useMutation({
  mutationFn: (id: string) => {
    syncSameAsPassword(editForm.value, editPreset.value);
    return withElevation(() => api.put<MailProvider>(`/mail-providers/${id}`, bodyOf(editForm.value)));
  },
  onSuccess: (_, id) => {
    clearRowError(id);
    editFor.value = null;
    qc.invalidateQueries({ queryKey: ["mail-providers"] });
  },
  onError: (e, id) => setRowError(id, e),
});

// ── delete ───────────────────────────────────────────────────────────────────────
const deleteProvider = useMutation({
  mutationFn: (id: string) => withElevation(() => api.del<void>(`/mail-providers/${id}`)),
  onSuccess: (_, id) => {
    clearRowError(id);
    confirmDeleteFor.value = null;
    qc.invalidateQueries({ queryKey: ["mail-providers"] });
  },
  onError: (e, id) => setRowError(id, e),
});

// ── test send ────────────────────────────────────────────────────────────────────
// Synchronous on the brain side (it dials the SMTP host), so this can take a
// few seconds; the button shows "Sending…" meanwhile. No elevation: it
// changes nothing.
const sendTest = useMutation({
  mutationFn: ({ id, to }: { id: string; to: string }) =>
    api.post<void>(`/mail-providers/${id}/test`, { to }),
  onSuccess: (_, { id, to }) => {
    clearRowError(id);
    testSent.value = { ...testSent.value, [id]: to };
    testFor.value = null;
    testTo.value = "";
  },
  onError: (e, { id }) => setRowError(id, e),
});

// One field idiom for the whole section, matching CustomInstallView: a labelled
// stack, one field per line, inset fill on the card with an olive focus ring.
const fieldClass =
  "w-full rounded-xl border border-border bg-background px-3 py-2 text-sm outline-none focus:border-accent";

// Every input is labelled, so each needs an id unique across the create form and
// any open edit row.
function fid(form: "new" | "edit", name: string, rowID = ""): string {
  return `mail-${form}-${name}${rowID ? `-${rowID}` : ""}`;
}
</script>

<template>
  <div class="space-y-6">
    <!-- Add provider -->
    <section class="space-y-3">
      <h2 class="text-xs font-medium uppercase tracking-wide text-muted-foreground">Outgoing email</h2>
      <p class="text-sm text-muted-foreground">
        Add an email account your apps can send from — password resets, reminders, invites. Apps choose an account when you install them.
      </p>

      <!-- Step 1: pick a provider. Each card is a real button — logo, name, and a
           visible hover / focus / active state, so it reads as clickable. -->
      <div v-if="!pickedPreset" class="space-y-3 rounded-2xl border border-border bg-card p-5 sm:p-6">
        <h3 class="text-sm font-semibold text-foreground">Who sends your email?</h3>
        <p class="text-xs text-muted-foreground">
          Pick your provider and malmo fills in the server settings for you.
        </p>
        <p v-if="presets.isLoading.value" class="text-sm text-muted-foreground">Loading…</p>
        <div v-else class="grid gap-3 sm:grid-cols-3">
          <button
            v-for="p in presetList"
            :key="p.id"
            type="button"
            class="flex cursor-pointer flex-col items-center justify-center gap-2 rounded-xl border border-border bg-background px-3 py-5 text-center transition-colors hover:border-accent hover:bg-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent focus-visible:ring-offset-2 focus-visible:ring-offset-card active:scale-[0.98]"
            @click="pickPreset(p)"
          >
            <MailProviderLogo :id="p.id" :label="p.label" />
            <span class="text-sm font-medium text-foreground">{{ p.label }}</span>
          </button>
        </div>
      </div>

      <!-- Step 2: fill in what the preset cannot know. One field per line, each
           with its own label and, where the name alone is not enough, a hint. -->
      <div v-else class="space-y-4 rounded-2xl border border-border bg-card p-5 sm:p-6">
        <div class="flex items-center justify-between gap-3">
          <div class="flex items-center gap-2.5">
            <MailProviderLogo :id="pickedPreset.id" :label="pickedPreset.label" />
            <h3 class="text-sm font-semibold text-foreground">{{ pickedPreset.label }}</h3>
          </div>
          <Button variant="ghost" size="sm" @click="cancelPick">Change provider</Button>
        </div>

        <p class="text-xs text-muted-foreground">
          {{ pickedPreset.help }}
          <a
            v-if="pickedPreset.docs_url"
            :href="pickedPreset.docs_url"
            target="_blank"
            rel="noopener noreferrer"
            class="underline"
          >Provider docs</a>
        </p>

        <!-- Account name. This was the confusing first field: it is malmo's own
             label for the account, not anything the provider knows about. -->
        <div class="space-y-1.5">
          <label class="text-sm font-medium" :for="fid('new', 'label')">Account name</label>
          <input
            :id="fid('new', 'label')"
            v-model="newForm.label"
            :class="fieldClass"
            autocomplete="off"
          />
          <p class="text-xs text-muted-foreground">
            What you'll see when an app asks which account to send from. Only you see this.
          </p>
        </div>

        <div class="space-y-1.5">
          <label class="text-sm font-medium" :for="fid('new', 'from')">From address</label>
          <input
            :id="fid('new', 'from')"
            v-model="newForm.from_address"
            type="email"
            placeholder="hello@example.com"
            :class="fieldClass"
            autocomplete="off"
          />
          <p class="text-xs text-muted-foreground">
            The address your apps' email will come from. Your provider has to allow it.
          </p>
        </div>

        <!-- Region: only the presets whose region changes the host carry one. -->
        <div v-if="pickedPreset.region" class="space-y-1.5">
          <label class="text-sm font-medium" :for="fid('new', 'region')">
            {{ pickedPreset.region.label }}
          </label>
          <select
            :id="fid('new', 'region')"
            v-model="newForm.region"
            :class="fieldClass"
            @change="onRegionChange(newForm)"
          >
            <option v-for="o in (pickedPreset.region.options ?? [])" :key="o.value" :value="o.value">
              {{ o.label }}
            </option>
          </select>
          <p class="text-xs text-muted-foreground">
            Pick the region your account is in — it decides which server malmo connects to.
          </p>
        </div>

        <!-- Username: hidden when the provider fixes it or reuses the credential. -->
        <div v-if="pickedPreset.username_mode === 'user'" class="space-y-1.5">
          <label class="text-sm font-medium" :for="fid('new', 'username')">Username</label>
          <input
            :id="fid('new', 'username')"
            v-model="newForm.username"
            :class="fieldClass"
            autocomplete="off"
          />
        </div>

        <div class="space-y-1.5">
          <label class="text-sm font-medium" :for="fid('new', 'password')">
            {{ pickedPreset.credential_label }}
          </label>
          <input
            :id="fid('new', 'password')"
            v-model="newForm.password"
            type="password"
            :class="fieldClass"
            autocomplete="new-password"
          />
        </div>

        <!-- Advanced: prefilled and editable, so a non-standard endpoint is never trapped.
             :open is a one-way binding read at mount only. That holds because this
             block is v-if-gated and remounts whenever the picked preset changes; a
             later feature that needs to force the disclosure open after mount must
             not reuse this binding, because Vue will not notice the user having
             toggled the native element underneath it. -->
        <details class="rounded-xl border border-border px-3 py-2" :open="newAdvancedOpen">
          <summary class="cursor-pointer text-sm font-medium text-muted-foreground">Server settings</summary>
          <div class="mt-3 space-y-4">
            <p class="text-xs text-muted-foreground">
              Filled in for you. Change these only if your provider gave you different values.
            </p>
            <div class="space-y-1.5">
              <label class="text-sm font-medium" :for="fid('new', 'host')">SMTP server</label>
              <input
                :id="fid('new', 'host')"
                v-model="newForm.host"
                placeholder="smtp.example.com"
                :class="fieldClass"
                autocomplete="off"
              />
            </div>
            <div class="space-y-1.5">
              <label class="text-sm font-medium" :for="fid('new', 'port')">Port</label>
              <input
                :id="fid('new', 'port')"
                v-model.number="newForm.port"
                type="number"
                min="1"
                max="65535"
                :class="fieldClass"
              />
            </div>
            <div class="space-y-1.5">
              <label class="text-sm font-medium" :for="fid('new', 'encryption')">Encryption</label>
              <select :id="fid('new', 'encryption')" v-model="newForm.encryption" :class="fieldClass">
                <option value="starttls">STARTTLS (usually port 587)</option>
                <option value="tls">TLS (usually port 465)</option>
                <option value="none">No encryption</option>
              </select>
            </div>
            <div v-if="pickedPreset.username_mode !== 'user'" class="space-y-1.5">
              <label class="text-sm font-medium" :for="fid('new', 'adv-username')">Username</label>
              <input
                :id="fid('new', 'adv-username')"
                v-model="newForm.username"
                :class="fieldClass"
                autocomplete="off"
              />
              <p class="text-xs text-muted-foreground">
                {{ pickedPreset.username_mode === "fixed"
                  ? `${pickedPreset.label} requires this exact value.`
                  : `${pickedPreset.label} uses the same value as the ${pickedPreset.credential_label.toLowerCase()}.` }}
              </p>
            </div>
          </div>
        </details>

        <p v-if="portWarning(newForm)" class="text-xs text-destructive">{{ portWarning(newForm) }}</p>

        <div class="flex items-center gap-3">
          <Button
            :disabled="create.isPending.value || !formValid(newForm)"
            @click="create.mutate()"
          >
            {{ create.isPending.value ? "Adding…" : "Add account" }}
          </Button>
          <p v-if="createError" class="text-xs text-destructive">{{ createError }}</p>
        </div>
      </div>
    </section>

    <!-- Provider list -->
    <section class="space-y-3">
      <h2 class="text-xs font-medium uppercase tracking-wide text-muted-foreground">Email accounts</h2>
      <p v-if="providers.isLoading.value" class="text-sm text-muted-foreground">Loading…</p>
      <p
        v-else-if="(providers.data.value?.providers ?? []).length === 0"
        class="text-sm text-muted-foreground"
      >
        No email accounts yet.
      </p>
      <ul v-else class="space-y-2">
        <li
          v-for="p in (providers.data.value?.providers ?? [])"
          :key="p.id"
          class="space-y-3 rounded-2xl border border-border bg-card p-5 sm:p-6"
        >
          <!-- Main row -->
          <div class="flex flex-wrap items-center gap-3">
            <MailProviderLogo :id="p.provider_type" :label="p.provider_label" />
            <div class="min-w-0 flex-1">
              <div class="text-sm font-medium">{{ p.label }}</div>
              <!-- The provider name reads better than the raw host; a hand-typed
                   account has no name to show, so it falls back to the host. -->
              <div class="text-xs text-muted-foreground">
                {{ p.from_address }} via
                {{ p.provider_type === "custom" ? `${p.host}:${p.port}` : p.provider_label }}
              </div>
            </div>
            <Button variant="secondary" size="sm" :disabled="sendTest.isPending.value" @click="startTest(p.id)">
              Send test
            </Button>
            <Button variant="secondary" size="sm" @click="startEdit(p)">Edit</Button>
            <Button
              variant="ghost"
              size="sm"
              class="text-destructive hover:bg-destructive/10"
              :disabled="deleteProvider.isPending.value"
              @click="editFor = null; testFor = null; confirmDeleteFor = confirmDeleteFor === p.id ? null : p.id"
            >
              Delete
            </Button>
          </div>

          <!-- Delete confirmation -->
          <div
            v-if="confirmDeleteFor === p.id"
            class="flex flex-wrap items-center gap-2 rounded-lg border border-destructive/40 bg-destructive/5 px-3 py-2"
          >
            <span class="text-sm">Delete <strong>{{ p.label }}</strong>? Apps using it will stop sending email until you pick another account for them.</span>
            <Button
              variant="secondary"
              size="sm"
              class="border-destructive text-destructive hover:bg-destructive/10"
              :disabled="deleteProvider.isPending.value"
              @click="deleteProvider.mutate(p.id)"
            >
              Delete
            </Button>
            <Button variant="ghost" size="sm" @click="confirmDeleteFor = null">Cancel</Button>
          </div>

          <!-- Inline test-send form -->
          <div v-if="testFor === p.id" class="flex flex-wrap gap-2">
            <div class="min-w-48 flex-1 space-y-1.5">
              <label class="text-sm font-medium" :for="fid('edit', 'testto', p.id)">Send a test email to</label>
              <input
                :id="fid('edit', 'testto', p.id)"
                v-model="testTo"
                type="email"
                placeholder="you@example.com"
                :class="fieldClass"
                autocomplete="off"
                @keydown.enter="!sendTest.isPending.value && testTo.includes('@') && sendTest.mutate({ id: p.id, to: testTo })"
              />
            </div>
            <Button
              class="mt-6"
              :disabled="sendTest.isPending.value || !testTo.includes('@')"
              @click="sendTest.mutate({ id: p.id, to: testTo })"
            >
              {{ sendTest.isPending.value ? "Sending…" : "Send" }}
            </Button>
            <Button variant="ghost" class="mt-6" @click="testFor = null">Cancel</Button>
          </div>

          <!-- Inline edit form: the same short form the account was created on,
               with the advanced fields open for a hand-typed account. -->
          <div v-if="editFor === p.id" class="space-y-3">
            <p v-if="editPreset && editPreset.id !== 'custom'" class="text-xs text-muted-foreground">
              {{ editPreset.help }}
              <a
                v-if="editPreset.docs_url"
                :href="editPreset.docs_url"
                target="_blank"
                rel="noopener noreferrer"
                class="underline"
              >Provider docs</a>
            </p>
            <div class="space-y-1.5">
              <label class="text-sm font-medium" :for="fid('edit', 'label', p.id)">Account name</label>
              <input
                :id="fid('edit', 'label', p.id)"
                v-model="editForm.label"
                :class="fieldClass"
                autocomplete="off"
              />
              <p class="text-xs text-muted-foreground">
                What you'll see when an app asks which account to send from. Only you see this.
              </p>
            </div>

            <div class="space-y-1.5">
              <label class="text-sm font-medium" :for="fid('edit', 'from', p.id)">From address</label>
              <input
                :id="fid('edit', 'from', p.id)"
                v-model="editForm.from_address"
                type="email"
                :class="fieldClass"
                autocomplete="off"
              />
            </div>

            <div v-if="!editPreset || editPreset.username_mode === 'user'" class="space-y-1.5">
              <label class="text-sm font-medium" :for="fid('edit', 'username', p.id)">Username</label>
              <input
                :id="fid('edit', 'username', p.id)"
                v-model="editForm.username"
                :class="fieldClass"
                autocomplete="off"
              />
            </div>

            <div class="space-y-1.5">
              <label class="text-sm font-medium" :for="fid('edit', 'password', p.id)">
                {{ editPreset?.credential_label ?? "Password" }}
              </label>
              <input
                :id="fid('edit', 'password', p.id)"
                v-model="editForm.password"
                type="password"
                :class="fieldClass"
                autocomplete="new-password"
              />
              <p class="text-xs text-muted-foreground">Leave blank to keep the one you saved.</p>
            </div>

            <details class="rounded-xl border border-border px-3 py-2" :open="editAdvancedOpen">
              <summary class="cursor-pointer text-sm font-medium text-muted-foreground">Server settings</summary>
              <div class="mt-3 space-y-4">
                <div class="space-y-1.5">
                  <label class="text-sm font-medium" :for="fid('edit', 'host', p.id)">SMTP server</label>
                  <input
                    :id="fid('edit', 'host', p.id)"
                    v-model="editForm.host"
                    :class="fieldClass"
                    autocomplete="off"
                  />
                </div>
                <div class="space-y-1.5">
                  <label class="text-sm font-medium" :for="fid('edit', 'port', p.id)">Port</label>
                  <input
                    :id="fid('edit', 'port', p.id)"
                    v-model.number="editForm.port"
                    type="number"
                    min="1"
                    max="65535"
                    :class="fieldClass"
                  />
                </div>
                <div class="space-y-1.5">
                  <label class="text-sm font-medium" :for="fid('edit', 'encryption', p.id)">Encryption</label>
                  <select :id="fid('edit', 'encryption', p.id)" v-model="editForm.encryption" :class="fieldClass">
                    <option value="starttls">STARTTLS (usually port 587)</option>
                    <option value="tls">TLS (usually port 465)</option>
                    <option value="none">No encryption</option>
                  </select>
                </div>
                <div v-if="editPreset && editPreset.username_mode !== 'user'" class="space-y-1.5">
                  <label class="text-sm font-medium" :for="fid('edit', 'adv-username', p.id)">Username</label>
                  <input
                    :id="fid('edit', 'adv-username', p.id)"
                    v-model="editForm.username"
                    :class="fieldClass"
                    autocomplete="off"
                  />
                </div>
              </div>
            </details>

            <p v-if="portWarning(editForm)" class="text-xs text-destructive">{{ portWarning(editForm) }}</p>
            <p class="text-xs text-muted-foreground">
              Apps already using this account pick up changes the next time they restart or rebind.
            </p>
            <div class="flex gap-2">
              <Button :disabled="update.isPending.value || !formValid(editForm)" @click="update.mutate(p.id)">
                {{ update.isPending.value ? "Saving…" : "Save" }}
              </Button>
              <Button variant="ghost" @click="editFor = null">Cancel</Button>
            </div>
          </div>

          <!-- Per-row test confirmation / error -->
          <p v-if="testSent[p.id]" class="text-xs text-muted-foreground">
            Test email sent to {{ testSent[p.id] }} — check that inbox.
          </p>
          <p v-if="rowError[p.id]" class="text-xs text-destructive">{{ rowError[p.id] }}</p>
        </li>
      </ul>
    </section>
  </div>
</template>
