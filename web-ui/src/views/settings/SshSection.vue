<script setup lang="ts">
// Settings → SSH — the signed-in user's own shell access (AUTH.md # Device
// access, issue #482). Every user manages their own; no admin manages it for
// them, which is why the routes sit under /me and this screen has no role gate.
//
// AUTH.md calls this panel "Device access (SSH + SMB)". SMB has no API yet, so
// the screen is named SSH and carries SSH alone. When file shares land they join
// this screen and the name goes back to Device access.
//
// The profile rule is NOT decided here. The brain answers it in key_required:
// true on hosted, where a public key is the mandatory factor, and false on the
// appliance, where the malmo password is. The server enforces both rows whatever
// this screen does, so reading the flag keeps the two from drifting.
//
// Every write is elevation-class server-side, so all three go through
// withElevation: the first one re-prompts for the password (or, for a hosted
// owner, takes a portal round-trip) and retries.
import { computed, ref } from "vue";
import { useQuery, useMutation, useQueryClient } from "@tanstack/vue-query";
import { SwitchRoot, SwitchThumb } from "reka-ui";
import { KeyRound, Trash2, Upload } from "lucide-vue-next";
import { api, ApiError, type SSHAccess } from "@/api";
import { withElevation } from "@/elevate";
import { isHosted, isBoxOwner, useAuth } from "@/auth";
import Button from "@/components/ui/Button.vue";

const qc = useQueryClient();
const { currentUser } = useAuth();

const ssh = useQuery({
  queryKey: ["ssh"],
  queryFn: () => api.get<SSHAccess>("/me/ssh"),
});

const access = computed(() => ssh.data.value);
// keys is nullable on the wire (an account with none serialises as null).
const keys = computed(() => access.value?.keys ?? []);
const keyRequired = computed(() => access.value?.key_required ?? false);
const enabled = computed(() => access.value?.enabled ?? false);
const requirePassword = computed(() => access.value?.require_password ?? false);

// Hosted refuses an enable with no key, so the switch stays off until there is
// one. The appliance always has the password behind it, so it never blocks.
const needsKeyFirst = computed(() => keyRequired.value && keys.value.length === 0);

// The hosted box owner cannot use the password lock. Their box password was
// generated during the portal handshake and thrown away (internal/api/sso.go),
// so nobody knows it and sshd would demand a string they cannot type. The brain
// does not guard this — it cannot tell "owner" from "wants a second lock" — so
// this screen is the only place it is handled. A box user the owner created does
// have a password and keeps the option.
const ownerHasNoPassword = computed(() => isHosted() && isBoxOwner());

const actionError = ref("");
const keyError = ref("");

function errorMessage(e: unknown): string {
  // A dismissed confirm prompt is a deliberate no-op, not a failure.
  if (e instanceof ApiError && e.code === "elevation_cancelled") return "";
  return e instanceof ApiError ? e.message : "Something went wrong.";
}

function onWritten(next: SSHAccess) {
  qc.setQueryData(["ssh"], next);
}

// ── the on/off switch and the password lock ────────────────────────────────────
// Both write the same endpoint, which takes the whole desired state. Sending the
// current value of the other field keeps a toggle from quietly resetting it.
const setAccess = useMutation({
  mutationFn: (body: { enabled: boolean; require_password: boolean }) =>
    withElevation(() => api.put<SSHAccess>("/me/ssh", body)),
  onSuccess: (next) => {
    actionError.value = "";
    onWritten(next);
  },
  onError: (e) => {
    actionError.value = errorMessage(e);
    // The switch is showing what the user clicked, which the box refused. Refetch
    // so it snaps back to the real state.
    qc.invalidateQueries({ queryKey: ["ssh"] });
  },
});

function toggleEnabled(on: boolean) {
  setAccess.mutate({ enabled: on, require_password: requirePassword.value });
}

function togglePassword(on: boolean) {
  setAccess.mutate({ enabled: enabled.value, require_password: on });
}

// ── keys ───────────────────────────────────────────────────────────────────────
const showAddKey = ref(false);
const keyText = ref("");
const keyLabel = ref("");
const fileInput = ref<HTMLInputElement | null>(null);

// A .pub file is read here and dropped into the same box the user could have
// pasted into, so both paths send one request shape and the server validates
// once. The server re-serialises and fingerprints the key, so nothing is parsed
// in the browser.
async function pickFile(event: Event) {
  const input = event.target as HTMLInputElement;
  const file = input.files?.[0];
  input.value = "";
  if (!file) return;
  keyError.value = "";
  keyText.value = (await file.text()).trim();
  if (!keyLabel.value) keyLabel.value = file.name.replace(/\.pub$/, "");
}

const addKey = useMutation({
  mutationFn: () =>
    withElevation(() =>
      api.post<SSHAccess>("/me/ssh/keys", {
        public_key: keyText.value,
        label: keyLabel.value.trim(),
      }),
    ),
  onSuccess: (next) => {
    keyError.value = "";
    keyText.value = "";
    keyLabel.value = "";
    showAddKey.value = false;
    onWritten(next);
  },
  onError: (e) => {
    keyError.value = errorMessage(e);
  },
});

const removeKey = useMutation({
  mutationFn: (id: string) => withElevation(() => api.del<void>(`/me/ssh/keys/${id}`)),
  onSuccess: () => {
    keyError.value = "";
    qc.invalidateQueries({ queryKey: ["ssh"] });
  },
  onError: (e) => {
    keyError.value = errorMessage(e);
  },
});

function cancelAddKey() {
  showAddKey.value = false;
  keyText.value = "";
  keyLabel.value = "";
  keyError.value = "";
}

function addedOn(seconds: number): string {
  return new Date(seconds * 1000).toLocaleDateString();
}

const busy = computed(
  () => setAccess.isPending.value || addKey.isPending.value || removeKey.isPending.value,
);
</script>

<template>
  <div class="space-y-6">
    <section class="space-y-3">
      <h2 class="text-xs font-medium uppercase tracking-wide text-muted-foreground">SSH</h2>
      <p class="text-sm text-muted-foreground">
        SSH gives you a command line on this box from another computer. It is off until you turn it
        on, and it only ever covers your own account.
      </p>

      <p v-if="ssh.isLoading.value" class="text-sm text-muted-foreground">Loading…</p>
      <p v-else-if="ssh.isError.value" class="text-sm text-destructive">
        Could not read your SSH settings. {{ (ssh.error.value as Error)?.message }}
      </p>

      <template v-else>
        <!-- The switch itself. -->
        <div class="flex items-center justify-between gap-4 rounded-xl border border-border bg-card px-4 py-3">
          <div class="min-w-0">
            <div class="text-sm font-medium">Allow SSH to my account</div>
            <div class="text-xs text-muted-foreground">
              <template v-if="needsKeyFirst">
                Add a public key below first. On this box a key is required, and a password on its
                own is not enough.
              </template>
              <template v-else-if="enabled">
                You can sign in from another computer as
                <span class="font-mono">{{ currentUser?.username }}</span>.
              </template>
              <template v-else>Nobody can reach this box over SSH while this is off.</template>
            </div>
          </div>
          <SwitchRoot
            :model-value="enabled"
            :disabled="busy || (needsKeyFirst && !enabled)"
            aria-label="Allow SSH to my account"
            class="relative inline-flex h-5 w-9 shrink-0 cursor-pointer items-center rounded-full border border-border bg-muted outline-none transition-colors disabled:cursor-default disabled:opacity-60 data-[state=checked]:border-accent data-[state=checked]:bg-accent"
            @update:model-value="toggleEnabled"
          >
            <SwitchThumb
              class="pointer-events-none block size-4 translate-x-0.5 rounded-full bg-card shadow transition-transform data-[state=checked]:translate-x-[1.125rem]"
            />
          </SwitchRoot>
        </div>

        <!-- The second factor. The wording says extra lock on purpose: both
             factors are demanded together, so neither one on its own lets anybody
             in. Calling it another sign-in method would describe the opposite. -->
        <div class="flex items-center justify-between gap-4 rounded-xl border border-border bg-card px-4 py-3">
          <div class="min-w-0">
            <div class="text-sm font-medium">
              {{ keyRequired ? "Also ask for my malmo password" : "Ask for my malmo password" }}
            </div>
            <div class="text-xs text-muted-foreground">
              <template v-if="!keyRequired">
                Always on. This box asks for your malmo password, and a key is an extra lock you can
                add on top.
              </template>
              <template v-else-if="ownerHasNoPassword">
                You sign in through the portal, so this box has no password for you to type. Your key
                is your way in.
              </template>
              <template v-else>
                An extra lock on top of your key, not another way in. With it on, you need both.
              </template>
            </div>
          </div>
          <SwitchRoot
            :model-value="requirePassword"
            :disabled="busy || !keyRequired || ownerHasNoPassword"
            aria-label="Ask for my malmo password"
            class="relative inline-flex h-5 w-9 shrink-0 cursor-pointer items-center rounded-full border border-border bg-muted outline-none transition-colors disabled:cursor-default disabled:opacity-60 data-[state=checked]:border-accent data-[state=checked]:bg-accent"
            @update:model-value="togglePassword"
          >
            <SwitchThumb
              class="pointer-events-none block size-4 translate-x-0.5 rounded-full bg-card shadow transition-transform data-[state=checked]:translate-x-[1.125rem]"
            />
          </SwitchRoot>
        </div>

        <p v-if="actionError" class="text-sm text-destructive">{{ actionError }}</p>
      </template>
    </section>

    <!-- Keys. Shown whatever the switch says, because on hosted you must add one
         before you can turn SSH on at all. -->
    <section v-if="!ssh.isLoading.value && !ssh.isError.value" class="space-y-3">
      <h2 class="text-xs font-medium uppercase tracking-wide text-muted-foreground">Your keys</h2>
      <p class="text-sm text-muted-foreground">
        A key is a file your computer holds. Add the public half here — the one ending in
        <span class="font-mono">.pub</span>. Never share the other file. Several keys is normal, one
        for each computer you use.
      </p>

      <ul v-if="keys.length" class="space-y-2">
        <li
          v-for="k in keys"
          :key="k.id"
          class="flex items-center justify-between gap-4 rounded-xl border border-border bg-card px-4 py-3"
        >
          <div class="flex min-w-0 items-center gap-3">
            <KeyRound class="size-5 shrink-0 text-muted-foreground" />
            <div class="min-w-0">
              <div class="truncate text-sm font-medium">{{ k.label || "Unnamed key" }}</div>
              <div class="truncate font-mono text-xs text-muted-foreground">{{ k.fingerprint }}</div>
              <div class="text-xs text-muted-foreground">Added {{ addedOn(k.added_at) }}</div>
            </div>
          </div>
          <Button
            variant="ghost"
            size="sm"
            :disabled="busy"
            :aria-label="`Remove ${k.label || 'key'}`"
            @click="removeKey.mutate(k.id)"
          >
            <Trash2 class="size-4" />
            Remove
          </Button>
        </li>
      </ul>
      <p v-else class="rounded-xl border border-dashed border-border px-4 py-3 text-sm text-muted-foreground">
        No keys yet.
      </p>

      <div v-if="!showAddKey">
        <Button variant="secondary" size="sm" @click="showAddKey = true">Add a key</Button>
      </div>

      <form v-else class="space-y-3 rounded-xl border border-border bg-card px-4 py-3" @submit.prevent="addKey.mutate()">
        <label class="block space-y-1">
          <span class="text-xs text-muted-foreground">Public key</span>
          <textarea
            v-model="keyText"
            rows="3"
            required
            spellcheck="false"
            placeholder="ssh-ed25519 AAAA… you@laptop"
            class="w-full rounded-lg border border-border bg-background px-3 py-1.5 font-mono text-xs outline-none focus:border-accent"
          ></textarea>
        </label>
        <div class="flex flex-wrap items-end gap-3">
          <label class="block min-w-0 flex-1 space-y-1">
            <span class="text-xs text-muted-foreground">Name it (optional)</span>
            <input
              v-model="keyLabel"
              type="text"
              placeholder="Laptop"
              class="w-full rounded-lg border border-border bg-background px-3 py-1.5 text-sm outline-none focus:border-accent"
            />
          </label>
          <Button type="button" variant="secondary" size="sm" @click="fileInput?.click()">
            <Upload class="size-4" />
            Choose a .pub file
          </Button>
          <input ref="fileInput" type="file" accept=".pub,text/plain" class="hidden" @change="pickFile" />
        </div>
        <p v-if="keyError" class="text-sm text-destructive">{{ keyError }}</p>
        <div class="flex justify-end gap-2">
          <Button type="button" variant="ghost" size="sm" @click="cancelAddKey">Cancel</Button>
          <Button type="submit" size="sm" :disabled="busy || !keyText.trim()">
            {{ addKey.isPending.value ? "Adding…" : "Add key" }}
          </Button>
        </div>
      </form>

      <p v-if="keyError && !showAddKey" class="text-sm text-destructive">{{ keyError }}</p>
    </section>
  </div>
</template>
