// cn() — the shadcn-vue class-merge helper. Combines clsx (conditional class
// lists) with tailwind-merge (last-wins conflict resolution). Present so
// shadcn components added via the CLI resolve their `@/lib/utils` import.
import { clsx, type ClassValue } from "clsx";
import { twMerge } from "tailwind-merge";

export function cn(...inputs: ClassValue[]): string {
  return twMerge(clsx(inputs));
}
