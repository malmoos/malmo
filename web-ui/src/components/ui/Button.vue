<script setup lang="ts">
// Shared pill button — the Oatmeal element idiom (rounded-full, olive ink fill),
// written fresh from Tailwind utilities (NOT transcribed from Oatmeal's
// button.tsx; the licence permits the pattern in our End Product, not the source).
// One source of truth for the app's primary/secondary/ghost buttons; every colour
// flows from the olive semantic tokens (WEB_UI.md # Styling). Native button attrs
// (type, disabled, @click, …) fall through to the root <button>.
import { computed, type HTMLAttributes } from "vue";
import { cn } from "@/lib/utils";

const props = withDefaults(
  defineProps<{
    variant?: "primary" | "secondary" | "ghost";
    size?: "sm" | "md" | "icon";
    class?: HTMLAttributes["class"];
  }>(),
  { variant: "primary", size: "md" },
);

const base =
  "inline-flex shrink-0 cursor-pointer items-center justify-center gap-1.5 rounded-full font-medium transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent focus-visible:ring-offset-2 focus-visible:ring-offset-background disabled:pointer-events-none disabled:opacity-60";

// Hover lightens the ink fill (olive-900 → olive-800), matching cloud's
// bg-olive-950 hover:bg-olive-800 CTAs — the one intentional literal-olive hover.
const variants: Record<NonNullable<typeof props.variant>, string> = {
  primary: "bg-accent text-accent-foreground hover:bg-olive-800",
  secondary: "border border-border bg-card text-foreground hover:bg-muted",
  ghost: "text-foreground hover:bg-muted",
};

const sizes: Record<NonNullable<typeof props.size>, string> = {
  sm: "px-3 py-1 text-sm/7",
  md: "px-4 py-2 text-sm/7",
  icon: "size-8 p-0 text-sm",
};

const classes = computed(() => cn(base, variants[props.variant], sizes[props.size], props.class));
</script>

<template>
  <button :class="classes">
    <slot />
  </button>
</template>
