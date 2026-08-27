<script setup lang="ts">
// Provider brand marks for the outgoing-email picker. Single-path, 24x24,
// monochrome, drawn in currentColor so they inherit the olive tokens and need
// no per-theme variant.
//
// The paths come from simple-icons, whose icon data is CC0; the trademarks stay
// with their owners and are used here only to identify the provider the admin is
// choosing. Bundled rather than fetched: a box may have no internet, and the
// dashboard must not call a CDN on render.
//
// Three providers have no simple-icons mark (Postmark, SMTP2GO, Google
// Workspace). They fall back to a lettermark tile, which reads as a logo slot
// rather than as a missing image. `custom` is not a brand at all, so it gets the
// Lucide server glyph.
import { computed } from "vue";
import { Server } from "lucide-vue-next";

const props = defineProps<{ id: string; label: string }>();

// Keyed by preset id (internal/mailpreset), not by simple-icons slug.
const paths: Record<string, string> = {
  ses: "M11.9996 0C5.3833 0 0 5.3834 0 11.9998c0 2.5316.7813 4.9544 2.2599 7.0051l.6955-.5014C1.5827 16.5993.8571 14.3505.8571 11.9998.8571 5.856 5.856.8572 12.0004.8572c6.144 0 11.1425 4.999 11.1425 11.1426 0 2.3508-.7256 4.5995-2.0983 6.5037l.6955.5014C23.2187 16.9542 24 14.5314 24 11.9998 24 5.3834 18.6163 0 11.9996 0zM6 16.7142a.4285.4285 0 0 0-.4286.4285v1.7598c-.9643.2048-1.7143 1.0822-1.7143 2.0974 0 1.1615.9815 2.143 2.1429 2.143s2.1429-.9815 2.1429-2.143c0-1.0152-.75-1.8926-1.7143-2.0974v-1.3312h5.1428v2.1883c-.9643.2049-1.7143 1.0822-1.7143 2.0975C9.8571 23.0186 10.8386 24 12 24s2.1429-.9814 2.1429-2.1429c0-1.0153-.75-1.8926-1.7143-2.0975v-2.1883h5.1428v1.3312c-.9643.2048-1.7143 1.0822-1.7143 2.0974 0 1.1615.9815 2.143 2.1429 2.143s2.1429-.9815 2.1429-2.143c0-1.0152-.75-1.8926-1.7143-2.0974v-1.7598A.4285.4285 0 0 0 18 16.7142h-5.5714v-2.5715H18c.237 0 .4286-.192.4286-.4286V5.9997A.4285.4285 0 0 0 18 5.571H6a.4285.4285 0 0 0-.4286.4286v7.7144c0 .2366.1916.4286.4286.4286h5.5714v2.5715H6zm1.2857 4.2857c0 .697-.5889 1.2858-1.2857 1.2858s-1.2857-.5889-1.2857-1.2858c0-.6968.5889-1.2857 1.2857-1.2857S7.2857 20.3031 7.2857 21zm12 0c0 .697-.5889 1.2858-1.2857 1.2858s-1.2857-.5889-1.2857-1.2858c0-.6968.5889-1.2857 1.2857-1.2857s1.2857.5889 1.2857 1.2857zm-1.7143-8.248L14.259 9.7703l3.3124-2.8389v5.8205zm-.7298-6.3236-4.842 4.1499-4.8412-4.15h9.6832zm-10.413.5031L9.741 9.7707 6.4286 12.752V6.9314zm.6878 6.3541 3.2807-2.9525 1.3239 1.135a.4253.4253 0 0 0 .2786.1032.4253.4253 0 0 0 .2785-.1033l1.3243-1.1349 3.2812 2.9525H7.1164zM12 20.5714c.6968 0 1.2857.5888 1.2857 1.2857 0 .6969-.5889 1.2857-1.2857 1.2857s-1.2857-.5888-1.2857-1.2857c0-.6969.5889-1.2857 1.2857-1.2857z",
  sendgrid: "M.8 24h13.6c.88 0 1.6-.72 1.6-1.6v-4.8c0-.88-.72-1.6-1.6-1.6H9.6c-.88 0-1.6-.72-1.6-1.6V9.6C8 8.72 7.28 8 6.4 8H1.6C.72 8 0 8.72 0 9.6v13.6c0 .44.36.8.8.8zM23.2 0H9.6C8.72 0 8 .72 8 1.6v4.8C8 7.28 8.72 8 9.6 8h4.8c.88 0 1.6.72 1.6 1.6v4.8c0 .88.72 1.6 1.6 1.6h4.8c.88 0 1.6-.72 1.6-1.6V.8c0-.44-.36-.8-.8-.8Z",
  mailgun: "M11.837 0c6.602 0 11.984 5.381 11.984 11.994-.017 2.99-3.264 4.84-5.844 3.331a3.805 3.805 0 0 1-.06-.035l-.055-.033-.022.055c-2.554 4.63-9.162 4.758-11.894.232-2.732-4.527.46-10.313 5.746-10.416a6.868 6.868 0 0 1 7.002 6.866 1.265 1.265 0 0 0 2.52 0c0-5.18-4.197-9.38-9.377-9.387C4.611 2.594.081 10.41 3.683 16.673c3.238 5.632 11.08 6.351 15.289 1.402l1.997 1.686A11.95 11.95 0 0 1 11.837 24C2.6 23.72-2.87 13.543 1.992 5.684A12.006 12.006 0 0 1 11.837 0Zm0 7.745c-3.276-.163-5.5 3.281-4.003 6.2a4.26 4.26 0 0 0 4.014 2.31c3.276-.171 5.137-3.824 3.35-6.575a4.26 4.26 0 0 0-3.36-1.935Zm0 2.53c1.324 0 2.152 1.433 1.49 2.58a1.72 1.72 0 0 1-1.49.86 1.72 1.72 0 1 1 0-3.44Z",
  brevo: "M12 0A12 12 0 0 0 0 12a12 12 0 0 0 12 12 12 12 0 0 0 12-12A12 12 0 0 0 12 0zM7.2 4.8h5.747c2.34 0 3.895 1.406 3.895 3.516 0 1.022-.348 1.862-1.09 2.588C17.189 11.812 18 13.22 18 14.785c0 2.86-2.64 5.016-6.164 5.016H7.199v-15zm2.085 1.952v5.537h.07c.233-.432.858-.796 2.249-1.226 2.039-.659 3.037-1.52 3.037-2.655 0-.998-.766-1.656-1.924-1.656H9.285zm4.87 5.266c-.766.385-1.67.748-2.76 1.11-1.229.387-2.11 1.386-2.11 2.407v2.315h2.365c2.387 0 4.149-1.34 4.149-3.155 0-1.067-.625-2.087-1.645-2.677z",
  resend: "M14.679 0c4.648 0 7.413 2.765 7.413 6.434s-2.765 6.434-7.413 6.434H12.33L24 24h-8.245l-8.88-8.44c-.636-.588-.93-1.273-.93-1.86 0-.831.587-1.565 1.713-1.883l4.574-1.224c1.737-.465 2.936-1.81 2.936-3.572 0-2.153-1.761-3.4-3.939-3.4H0V0z",
};

// Initials for the providers with no brand mark. Two characters at most —
// "SG" reads as a mark, "SMTP2GO" reads as truncated text.
const letters: Record<string, string> = {
  postmark: "P",
  smtp2go: "S2",
  google_workspace: "W",
};

const path = computed(() => paths[props.id]);
const letter = computed(() => letters[props.id]);
</script>

<template>
  <svg
    v-if="path"
    class="size-7"
    viewBox="0 0 24 24"
    fill="currentColor"
    role="img"
    :aria-label="label"
  >
    <path :d="path" />
  </svg>
  <span
    v-else-if="letter"
    class="flex size-7 items-center justify-center rounded-lg bg-muted text-xs font-semibold text-muted-foreground"
    :aria-label="label"
    role="img"
  >{{ letter }}</span>
  <Server v-else class="size-7 stroke-[1.5]" :aria-label="label" role="img" />
</template>
