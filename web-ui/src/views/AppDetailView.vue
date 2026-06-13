<script setup lang="ts">
// App detail page (/store/:id) — the catalog app's full view, structured like an
// app-store product page (header → screenshots → description → info). This is
// where Install lives (the browse grid only navigates here): the consent flow,
// duplicate handling, and household/personal split-button are driven by the
// useInstall composable, shared in shape with what the Store row used to do.
//
// The long description is author markdown rendered to HTML and sanitized before
// it touches the DOM (catalog text is author-controlled; sanitize anyway).
import { computed, ref, watch } from "vue";
import { useRoute, RouterLink } from "vue-router";
import { useQuery } from "@tanstack/vue-query";
import { marked } from "marked";
import DOMPurify from "dompurify";
import { Loader2 } from "lucide-vue-next";
import { api, type CatalogDetail } from "../api";
import { useInstall } from "../useInstall";
import { formatSize, safeExternalUrl } from "../utils";
import AppGlyph from "../components/AppGlyph.vue";
import InstallDialog from "../components/InstallDialog.vue";
import SplitButton from "../components/SplitButton.vue";
import HealthGated from "../components/HealthGated.vue";

const route = useRoute();
// The route param is the manifest id; keep it reactive so navigating between two
// detail pages without unmounting re-drives the queries.
const manifestId = computed(() => String(route.params.id));

const detailQuery = useQuery({
  queryKey: computed(() => ["catalog", manifestId.value]),
  queryFn: () => api.get<CatalogDetail>(`/catalog/${manifestId.value}`),
});
const app = computed(() => detailQuery.data.value ?? null);

const {
  activePlan,
  dialogScope,
  dialogError,
  duplicateInfo,
  installError,
  installing,
  currentStep,
  install,
  openInstallDialog,
  closeDialog,
  handleSubmit,
  handleConfirmDuplicate,
  householdInstance,
  ownPersonalInstance,
  dropdownItems,
} = useInstall(manifestId);

// The brain emits a fine-grained `step` throughout the install job
// (internal/lifecycle/lifecycle.go). Collapse those ~15 technical steps into a
// few friendly, non-technical phases for the Install button — wording stays in
// the view (useInstall exposes the raw step). Any unknown or empty step falls
// back to the generic "Installing…" so a newly-added brain step never surfaces
// raw on the button.
const INSTALL_PHASES: Record<string, string> = {
  admitting_compose: "Preparing…",
  checking_gpu: "Preparing…",
  allocating_slug: "Preparing…",
  writing_instance_dir: "Preparing…",
  generating_secrets: "Preparing…",
  provisioning_services: "Preparing…",
  binding_mail_provider: "Preparing…",
  generating_override: "Preparing…",
  creating_network: "Preparing…",
  publishing_mdns: "Preparing…",
  registering_route: "Preparing…",
  resolving_digests: "Downloading…",
  compose_up: "Downloading…",
  waiting_healthy: "Starting…",
  flipping_route: "Starting…",
};
const installPhaseLabel = computed(
  () => (currentStep.value && INSTALL_PHASES[currentStep.value]) || "Installing…",
);

// brokenIcon falls the header icon back to the glyph if the asset fails to load;
// reset when navigating to a different app so a fresh icon gets a fresh chance.
const brokenIcon = ref(false);
watch(manifestId, () => { brokenIcon.value = false; });

// Rendered, sanitized markdown body. Empty when the manifest carries no long
// description (the section is hidden in that case).
const descriptionHtml = computed(() => {
  const md = app.value?.long_description;
  if (!md) return "";
  return DOMPurify.sanitize(marked.parse(md, { async: false }) as string);
});

// sizeLabel is the coarse catalog footprint (image disk bytes); shown only when
// the manifest carries sized images. The install dialog shows the sharper,
// box-specific figure (DASHBOARD.md # the consent screen shows the on-disk footprint).
const sizeLabel = computed(() => {
  const b = app.value?.footprint.image_disk_bytes ?? 0;
  return b > 0 ? formatSize(b) : null;
});

// External links are app-provided; pass them through safeExternalUrl so a
// non-http(s) value (e.g. a javascript: URL) is dropped rather than bound to an
// :href. The template renders a link only when the vetted URL is present.
const authorUrl = computed(() => safeExternalUrl(app.value?.author?.url));
const homepageUrl = computed(() => safeExternalUrl(app.value?.links?.homepage));
const sourceUrl = computed(() => safeExternalUrl(app.value?.links?.source));
const supportUrl = computed(() => safeExternalUrl(app.value?.links?.support));
const changelogUrl = computed(() => safeExternalUrl(app.value?.changelog_url));
const hasLinks = computed(
  () => !!(homepageUrl.value || sourceUrl.value || supportUrl.value || changelogUrl.value),
);
</script>

<template>
  <div class="space-y-8 pt-2">
    <RouterLink to="/store" class="inline-block text-sm text-muted-foreground hover:text-foreground">
      ← Store
    </RouterLink>

    <p v-if="detailQuery.isLoading.value" class="text-sm text-muted-foreground">Loading…</p>
    <p v-else-if="detailQuery.isError.value" class="text-sm text-destructive">
      Couldn't load this app. {{ (detailQuery.error.value as Error)?.message }}
    </p>

    <template v-else-if="app">
      <!-- Header: icon · name/tagline/developer · Install -->
      <header class="flex flex-col gap-5 sm:flex-row sm:items-center">
        <div
          class="grid size-20 shrink-0 place-items-center overflow-hidden rounded-3xl border border-border bg-card text-muted-foreground"
        >
          <img
            v-if="app.icon_url && !brokenIcon"
            :src="app.icon_url"
            :alt="`${app.name} icon`"
            class="size-full object-cover"
            @error="brokenIcon = true"
          />
          <AppGlyph v-else :name="app.icon_glyph" class="size-9" />
        </div>

        <div class="min-w-0 flex-1">
          <h1 class="text-xl font-semibold">{{ app.name }}</h1>
          <p v-if="app.short_description" class="mt-0.5 text-sm text-muted-foreground">
            {{ app.short_description }}
          </p>
          <p v-if="app.author?.name" class="mt-1 text-xs text-muted-foreground">
            by
            <a
              v-if="authorUrl"
              :href="authorUrl"
              target="_blank"
              rel="noopener noreferrer"
              class="hover:text-foreground hover:underline"
            >{{ app.author.name }}</a>
            <span v-else>{{ app.author.name }}</span>
          </p>
        </div>

        <!-- Install / Open affordance — same state machine as the old Store row. -->
        <div class="flex shrink-0 items-center gap-2">
          <a
            v-if="householdInstance"
            :href="householdInstance.url"
            target="_blank"
            rel="noopener"
            class="rounded-lg border border-border px-3 py-1.5 text-sm hover:bg-muted"
          >
            Open shared app
          </a>
          <a
            v-else-if="ownPersonalInstance"
            :href="ownPersonalInstance.url"
            target="_blank"
            rel="noopener"
            class="rounded-lg bg-accent px-4 py-1.5 text-sm font-medium text-accent-foreground hover:opacity-90"
          >
            Open
          </a>

          <!-- Install button: shown when no own instance exists, or alongside
               "Open shared app" so the caller can still install their own copy. -->
          <HealthGated v-if="!ownPersonalInstance" blocks="apps">
            <SplitButton
              :label="installing ? installPhaseLabel : 'Install'"
              :loading="installing"
              :disabled="installing"
              :items="dropdownItems"
              @click="openInstallDialog()"
            />
          </HealthGated>
        </div>
      </header>

      <!-- Screenshots gallery -->
      <section v-if="app.screenshot_urls?.length" class="space-y-3">
        <div class="-mx-4 flex snap-x gap-4 overflow-x-auto px-4 pb-2">
          <img
            v-for="(src, i) in app.screenshot_urls"
            :key="src"
            :src="src"
            :alt="`${app.name} screenshot ${i + 1}`"
            class="h-56 shrink-0 snap-start rounded-xl border border-border object-cover"
          />
        </div>
      </section>

      <div class="grid gap-8 lg:grid-cols-[1fr_16rem]">
        <!-- Long description -->
        <section v-if="descriptionHtml" class="markdown-body min-w-0" v-html="descriptionHtml" />
        <p v-else class="text-sm text-muted-foreground">No description provided.</p>

        <!-- Info panel -->
        <aside class="space-y-3 text-sm">
          <h2 class="text-xs font-medium uppercase tracking-wide text-muted-foreground">Information</h2>
          <dl class="space-y-2">
            <div class="flex justify-between gap-4">
              <dt class="text-muted-foreground">Version</dt>
              <dd class="text-right">{{ app.version }}</dd>
            </div>
            <div v-if="app.categories?.length" class="flex justify-between gap-4">
              <dt class="text-muted-foreground">Category</dt>
              <dd class="text-right capitalize">{{ app.categories.join(", ") }}</dd>
            </div>
            <div v-if="sizeLabel" class="flex justify-between gap-4">
              <dt class="text-muted-foreground">Size</dt>
              <dd class="text-right">{{ sizeLabel }}</dd>
            </div>
            <div v-if="app.license" class="flex justify-between gap-4">
              <dt class="text-muted-foreground">License</dt>
              <dd class="text-right">{{ app.license }}</dd>
            </div>
          </dl>

          <div v-if="hasLinks" class="space-y-1.5 border-t border-border pt-3">
            <a v-if="homepageUrl" :href="homepageUrl" target="_blank" rel="noopener noreferrer" class="block text-accent hover:underline">Website</a>
            <a v-if="sourceUrl" :href="sourceUrl" target="_blank" rel="noopener noreferrer" class="block text-accent hover:underline">Source code</a>
            <a v-if="supportUrl" :href="supportUrl" target="_blank" rel="noopener noreferrer" class="block text-accent hover:underline">Support</a>
            <a v-if="changelogUrl" :href="changelogUrl" target="_blank" rel="noopener noreferrer" class="block text-accent hover:underline">Changelog</a>
          </div>
        </aside>
      </div>

      <!-- Duplicate-install warning (409 duplicate-install) -->
      <div v-if="duplicateInfo" class="rounded-xl border border-border bg-card px-4 py-3 space-y-2">
        <p class="text-sm">{{ duplicateInfo }}</p>
        <div class="flex gap-2">
          <button
            class="inline-flex items-center rounded-lg border border-border px-3 py-1.5 text-sm hover:bg-muted disabled:opacity-50"
            :disabled="installing"
            @click="handleConfirmDuplicate"
          >
            <Loader2 v-if="installing" class="mr-1.5 size-4 animate-spin" aria-hidden="true" />
            {{ installing ? installPhaseLabel : "Install my own copy" }}
          </button>
          <button class="rounded-lg border border-border px-3 py-1.5 text-sm hover:bg-muted" @click="closeDialog">
            Cancel
          </button>
        </div>
      </div>

      <!-- Install failed after the dialog closed (job failure / host 5xx) -->
      <div v-if="installError" class="rounded-xl border border-destructive/40 bg-destructive/10 px-4 py-3 space-y-2">
        <p class="text-sm text-destructive">{{ installError }}</p>
        <button class="rounded-lg border border-border px-3 py-1.5 text-sm hover:bg-muted" @click="installError = null">
          Dismiss
        </button>
      </div>

      <!-- Install consent dialog -->
      <InstallDialog
        v-if="activePlan && !duplicateInfo && !install.isPending.value"
        :plan="activePlan"
        :scope="dialogScope"
        :submit-error="dialogError"
        @submit="handleSubmit"
        @cancel="closeDialog"
      />
    </template>
  </div>
</template>

<style scoped>
/* Minimal markdown styling — Tailwind preflight strips heading/list defaults, and
   the project has no typography plugin. Scoped, so :deep() reaches the v-html
   subtree. Keeps the body readable without pulling in @tailwindcss/typography. */
.markdown-body :deep(h1),
.markdown-body :deep(h2),
.markdown-body :deep(h3) {
  font-weight: 600;
  line-height: 1.3;
  margin: 1.25rem 0 0.5rem;
}
.markdown-body :deep(h1) { font-size: 1.25rem; }
.markdown-body :deep(h2) { font-size: 1.1rem; }
.markdown-body :deep(h3) { font-size: 1rem; }
.markdown-body :deep(p) { margin: 0.75rem 0; line-height: 1.6; }
.markdown-body :deep(ul),
.markdown-body :deep(ol) { margin: 0.75rem 0; padding-left: 1.5rem; }
.markdown-body :deep(ul) { list-style: disc; }
.markdown-body :deep(ol) { list-style: decimal; }
.markdown-body :deep(li) { margin: 0.25rem 0; }
.markdown-body :deep(a) { color: var(--accent, #2563eb); text-decoration: underline; }
.markdown-body :deep(code) {
  font-family: ui-monospace, monospace;
  font-size: 0.85em;
  background: var(--muted, #f1f3f5);
  padding: 0.1em 0.3em;
  border-radius: 0.25rem;
}
.markdown-body :deep(pre) {
  background: var(--muted, #f1f3f5);
  padding: 0.75rem;
  border-radius: 0.5rem;
  overflow-x: auto;
}
.markdown-body :deep(pre code) { background: none; padding: 0; }
.markdown-body :deep(strong) { font-weight: 600; }
</style>
