<script setup lang="ts">
import { DropdownMenuRoot, DropdownMenuTrigger, DropdownMenuPortal, DropdownMenuContent, DropdownMenuItem } from "reka-ui";
import { ChevronDown } from "lucide-vue-next";

defineProps<{
  label: string;
  disabled?: boolean;
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
      class="relative inline-flex items-center rounded-l-lg border border-border bg-card px-3 py-1.5 text-sm hover:bg-muted disabled:opacity-50"
      :class="items?.length ? '' : 'rounded-r-lg'"
      @click="emit('click')"
    >
      {{ label }}
    </button>

    <DropdownMenuRoot v-if="items?.length">
      <DropdownMenuTrigger
        :disabled="disabled"
        class="relative -ml-px inline-flex items-center rounded-r-lg border border-border bg-card px-2 py-1.5 text-muted-foreground hover:bg-muted disabled:opacity-50 focus:z-10"
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
