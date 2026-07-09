<script setup lang="ts">
// Settings → Outgoing email — admin-only SMTP provider management
// (SERVICE_PROVISIONING.md # BYO outgoing mail, issue #122). Apps that can send
// email bind to one of these providers at install time (or later from their
// detail page); the brain injects the credentials as MALMO_MAIL_* env vars.
// Mirrors UsersSection: admin redirect as defence in depth, every mutation
// wrapped in withElevation, guard rejections surface as inline errors. The
// test-send is the one non-elevated action (it changes nothing).
import { ref, watch } from "vue";
import { useRouter } from "vue-router";
import { useQuery, useMutation, useQueryClient } from "@tanstack/vue-query";
import { api, ApiError, type MailProvider } from "@/api";
import { withElevation } from "@/elevate";
import { useAuth } from "@/auth";
import Heading from "@/components/ui/Heading.vue";

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

// ── provider list ───────────────────────────────────────────────────────────────
const providers = useQuery({
  queryKey: ["mail-providers"],
  queryFn: () => api.get<{ providers: MailProvider[] }>("/mail-providers"),
});

// ── shared form shape (create + edit) ───────────────────────────────────────────
type ProviderForm = {
  label: string;
  host: string;
  port: number;
  username: string;
  password: string;
  from_address: string;
  encryption: "none" | "starttls" | "tls";
};

function emptyForm(): ProviderForm {
  return { label: "", host: "", port: 587, username: "", password: "", from_address: "", encryption: "starttls" };
}

function formValid(f: ProviderForm): boolean {
  return !!f.label.trim() && !!f.host.trim() && f.port >= 1 && f.port <= 65535 && f.from_address.includes("@");
}

function errorMessage(e: unknown): string {
  // A dismissed elevation prompt is a deliberate no-op, not a failure.
  if (e instanceof ApiError && e.code === "elevation_cancelled") return "";
  return e instanceof ApiError ? e.message : "Something went wrong.";
}

// ── create ───────────────────────────────────────────────────────────────────────
const newForm = ref<ProviderForm>(emptyForm());
const createError = ref<string | null>(null);

const create = useMutation({
  mutationFn: () => withElevation(() => api.post<MailProvider>("/mail-providers", { ...newForm.value })),
  onSuccess: () => {
    newForm.value = emptyForm();
    createError.value = null;
    qc.invalidateQueries({ queryKey: ["mail-providers"] });
  },
  onError: (e) => {
    createError.value = errorMessage(e);
  },
});

// ── per-row state ────────────────────────────────────────────────────────────────
// Only one expanded panel (edit / test / delete-confirm) per row at a time.
const editFor = ref<string | null>(null);
const editForm = ref<ProviderForm>(emptyForm());
const testFor = ref<string | null>(null);
const testTo = ref("");
const testSent = ref<Record<string, string>>({}); // id → "sent to <addr>" confirmation
const confirmDeleteFor = ref<string | null>(null);
const rowError = ref<Record<string, string>>({});

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
  };
}

function startTest(id: string) {
  editFor.value = null;
  confirmDeleteFor.value = null;
  testFor.value = testFor.value === id ? null : id;
  testTo.value = "";
}

// ── update ───────────────────────────────────────────────────────────────────────
const update = useMutation({
  mutationFn: (id: string) => withElevation(() => api.put<MailProvider>(`/mail-providers/${id}`, { ...editForm.value })),
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
</script>

<template>
  <div class="space-y-6">
    <!-- Add provider form -->
    <section class="space-y-4">
      <Heading :level="2">Outgoing email</Heading>
      <p class="text-sm text-muted-foreground">
        Add an email account your apps can send from — password resets, reminders, invites. Apps choose an account when you install them.
      </p>
      <div class="rounded-2xl border border-border bg-card p-5 space-y-3">
        <div class="grid gap-2 sm:grid-cols-2">
          <input
            v-model="newForm.label"
            placeholder="Name (e.g. Fastmail)"
            class="rounded-lg border border-border bg-background px-3 py-1.5 text-sm outline-none focus:border-accent"
            autocomplete="off"
          />
          <input
            v-model="newForm.from_address"
            placeholder="From address (e.g. box@example.com)"
            class="rounded-lg border border-border bg-background px-3 py-1.5 text-sm outline-none focus:border-accent"
            autocomplete="off"
          />
          <input
            v-model="newForm.host"
            placeholder="SMTP server (e.g. smtp.fastmail.com)"
            class="rounded-lg border border-border bg-background px-3 py-1.5 text-sm outline-none focus:border-accent"
            autocomplete="off"
          />
          <div class="flex gap-2">
            <input
              v-model.number="newForm.port"
              type="number"
              min="1"
              max="65535"
              placeholder="Port"
              class="w-24 rounded-lg border border-border bg-background px-3 py-1.5 text-sm outline-none focus:border-accent"
            />
            <select
              v-model="newForm.encryption"
              class="flex-1 rounded-lg border border-border bg-background px-3 py-1.5 text-sm outline-none focus:border-accent"
            >
              <option value="starttls">STARTTLS (usually port 587)</option>
              <option value="tls">TLS (usually port 465)</option>
              <option value="none">No encryption</option>
            </select>
          </div>
          <input
            v-model="newForm.username"
            placeholder="Username (optional)"
            class="rounded-lg border border-border bg-background px-3 py-1.5 text-sm outline-none focus:border-accent"
            autocomplete="off"
          />
          <input
            v-model="newForm.password"
            type="password"
            placeholder="Password (optional)"
            class="rounded-lg border border-border bg-background px-3 py-1.5 text-sm outline-none focus:border-accent"
            autocomplete="new-password"
          />
        </div>
        <div class="flex items-center gap-3">
          <button
            class="rounded-lg border border-border px-4 py-1.5 text-sm hover:bg-muted disabled:opacity-50"
            :disabled="create.isPending.value || !formValid(newForm)"
            @click="create.mutate()"
          >
            Add
          </button>
          <p v-if="createError" class="text-xs text-destructive">{{ createError }}</p>
        </div>
      </div>
    </section>

    <!-- Provider list -->
    <section class="space-y-4">
      <Heading :level="2">Email accounts</Heading>
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
          class="rounded-2xl border border-border bg-card p-5 space-y-3"
        >
          <!-- Main row -->
          <div class="flex flex-wrap items-center gap-2">
            <div class="min-w-0 flex-1">
              <div class="text-sm font-medium">{{ p.label }}</div>
              <div class="text-xs text-muted-foreground">{{ p.from_address }} via {{ p.host }}:{{ p.port }}</div>
            </div>
            <button
              class="rounded-lg border border-border px-3 py-1 text-sm hover:bg-muted disabled:opacity-50"
              :disabled="sendTest.isPending.value"
              @click="startTest(p.id)"
            >
              Send test
            </button>
            <button
              class="rounded-lg border border-border px-3 py-1 text-sm hover:bg-muted"
              @click="startEdit(p)"
            >
              Edit
            </button>
            <button
              class="rounded-lg border border-border px-3 py-1 text-sm text-destructive hover:bg-muted disabled:opacity-50"
              :disabled="deleteProvider.isPending.value"
              @click="editFor = null; testFor = null; confirmDeleteFor = confirmDeleteFor === p.id ? null : p.id"
            >
              Delete
            </button>
          </div>

          <!-- Delete confirmation -->
          <div
            v-if="confirmDeleteFor === p.id"
            class="flex flex-wrap items-center gap-2 rounded-lg border border-destructive/40 bg-destructive/5 px-3 py-2"
          >
            <span class="text-sm">Delete <strong>{{ p.label }}</strong>? Apps using it will stop sending email until you pick another account for them.</span>
            <button
              class="rounded-lg border border-destructive px-3 py-1 text-sm text-destructive hover:bg-destructive/10 disabled:opacity-50"
              :disabled="deleteProvider.isPending.value"
              @click="deleteProvider.mutate(p.id)"
            >
              Delete
            </button>
            <button
              class="rounded-lg border border-border px-3 py-1 text-sm hover:bg-muted"
              @click="confirmDeleteFor = null"
            >
              Cancel
            </button>
          </div>

          <!-- Inline test-send form -->
          <div v-if="testFor === p.id" class="flex flex-wrap gap-2">
            <input
              v-model="testTo"
              type="email"
              placeholder="Send a test email to…"
              class="min-w-48 flex-1 rounded-lg border border-border bg-background px-3 py-1.5 text-sm outline-none focus:border-accent"
              autocomplete="off"
              @keydown.enter="!sendTest.isPending.value && testTo.includes('@') && sendTest.mutate({ id: p.id, to: testTo })"
            />
            <button
              class="rounded-lg border border-border px-3 py-1.5 text-sm hover:bg-muted disabled:opacity-50"
              :disabled="sendTest.isPending.value || !testTo.includes('@')"
              @click="sendTest.mutate({ id: p.id, to: testTo })"
            >
              {{ sendTest.isPending.value ? "Sending…" : "Send" }}
            </button>
            <button
              class="rounded-lg border border-border px-3 py-1.5 text-sm hover:bg-muted"
              @click="testFor = null"
            >
              Cancel
            </button>
          </div>

          <!-- Inline edit form -->
          <div v-if="editFor === p.id" class="space-y-3">
            <div class="grid gap-2 sm:grid-cols-2">
              <input
                v-model="editForm.label"
                placeholder="Name"
                class="rounded-lg border border-border bg-background px-3 py-1.5 text-sm outline-none focus:border-accent"
                autocomplete="off"
              />
              <input
                v-model="editForm.from_address"
                placeholder="From address"
                class="rounded-lg border border-border bg-background px-3 py-1.5 text-sm outline-none focus:border-accent"
                autocomplete="off"
              />
              <input
                v-model="editForm.host"
                placeholder="SMTP server"
                class="rounded-lg border border-border bg-background px-3 py-1.5 text-sm outline-none focus:border-accent"
                autocomplete="off"
              />
              <div class="flex gap-2">
                <input
                  v-model.number="editForm.port"
                  type="number"
                  min="1"
                  max="65535"
                  placeholder="Port"
                  class="w-24 rounded-lg border border-border bg-background px-3 py-1.5 text-sm outline-none focus:border-accent"
                />
                <select
                  v-model="editForm.encryption"
                  class="flex-1 rounded-lg border border-border bg-background px-3 py-1.5 text-sm outline-none focus:border-accent"
                >
                  <option value="starttls">STARTTLS (usually port 587)</option>
                  <option value="tls">TLS (usually port 465)</option>
                  <option value="none">No encryption</option>
                </select>
              </div>
              <input
                v-model="editForm.username"
                placeholder="Username (optional)"
                class="rounded-lg border border-border bg-background px-3 py-1.5 text-sm outline-none focus:border-accent"
                autocomplete="off"
              />
              <input
                v-model="editForm.password"
                type="password"
                placeholder="Password (unchanged if blank)"
                class="rounded-lg border border-border bg-background px-3 py-1.5 text-sm outline-none focus:border-accent"
                autocomplete="new-password"
              />
            </div>
            <p class="text-xs text-muted-foreground">
              Apps already using this account pick up changes the next time they restart or rebind.
            </p>
            <div class="flex gap-2">
              <button
                class="rounded-lg border border-border px-3 py-1.5 text-sm hover:bg-muted disabled:opacity-50"
                :disabled="update.isPending.value || !formValid(editForm)"
                @click="update.mutate(p.id)"
              >
                Save
              </button>
              <button
                class="rounded-lg border border-border px-3 py-1.5 text-sm hover:bg-muted"
                @click="editFor = null"
              >
                Cancel
              </button>
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
