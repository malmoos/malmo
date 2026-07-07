<script setup lang="ts">
import { DropdownMenuRoot, DropdownMenuTrigger, DropdownMenuPortal, DropdownMenuContent, DropdownMenuItem } from "reka-ui";
import { ChevronDown, Loader2 } from "lucide-vue-next";

defineProps<{
  label: string;
  disabled?: boolean;
  // loading shows a spinner before the label (e.g. a running install, #150).
  loading?: boolean;
  // When items is empty the chevron is hidden and this renders as a plain button.
  items?: { label: string; action: () => void }[];
}>();

const emit = defineEmits<{ click: [] }>();
</script>

<template>
  <div class="inline-flex rounded-lg shadow-sm">
    <button
      type="button"
      :disabled="disabled"
      class="relative inline-flex cursor-pointer items-center rounded-l-lg bg-accent px-3 py-1.5 text-sm font-medium text-accent-foreground transition-colors hover:bg-olive-800 disabled:cursor-not-allowed disabled:opacity-50"
      :class="items?.length ? '' : 'rounded-r-lg'"
      @click="emit('click')"
    >
      <Loader2 v-if="loading" class="mr-1.5 size-4 animate-spin" aria-hidden="true" />
      {{ label }}
    </button>

    <DropdownMenuRoot v-if="items?.length">
      <DropdownMenuTrigger
        :disabled="disabled"
        class="relative inline-flex cursor-pointer items-center rounded-r-lg border-l border-accent-foreground/20 bg-accent px-2 py-1.5 text-accent-foreground transition-colors hover:bg-olive-800 disabled:cursor-not-allowed disabled:opacity-50 focus:z-10"
        aria-label="More install options"
      >
        <ChevronDown class="size-4" aria-hidden="true" />
      </DropdownMenuTrigger>

      <DropdownMenuPortal>
        <DropdownMenuContent
          align="end"
          :side-offset="4"
          class="z-50 min-w-44 rounded-xl border border-border bg-card py-1 shadow-lg"
        >
          <DropdownMenuItem
            v-for="item in items"
            :key="item.label"
            class="cursor-pointer px-4 py-2 text-sm outline-none hover:bg-muted data-[highlighted]:bg-muted"
            @click="item.action()"
          >
            {{ item.label }}
          </DropdownMenuItem>
        </DropdownMenuContent>
      </DropdownMenuPortal>
    </DropdownMenuRoot>
  </div>
</template>
