// useNotifications wraps the /api/v1/notifications read surface
// (NOTIFICATIONS.md # Surfaces) as TanStack Query state — the dashboard-bell
// sibling of the catalog/apps queries in Dashboard.vue. The list and unread
// count are pull; useEvents pushes cache invalidations on
// notification.created / notification.updated so the bell updates live without
// owning its own EventSource (WEB_UI.md: push and pull share one cache).
import { useQuery, useMutation, useQueryClient } from "@tanstack/vue-query";
import { api, type Notification } from "./api";
import { pushErrorToast } from "./toasts";

export function useNotifications() {
  const qc = useQueryClient();
  // Both queries live under the ["notifications"] prefix so a single
  // invalidateQueries refetches the list and the badge together.
  const invalidate = () => qc.invalidateQueries({ queryKey: ["notifications"] });

  const list = useQuery({
    queryKey: ["notifications", "list"],
    queryFn: () => api.get<{ notifications: Notification[] }>("/notifications"),
  });

  // Dedicated count endpoint, not derived from the (paged) list: the badge must
  // reflect every unread row, including ones past the first page.
  const unreadCount = useQuery({
    queryKey: ["notifications", "unread-count"],
    queryFn: () => api.get<{ count: number }>("/notifications/unread-count"),
  });

  // These mutations have no optimistic onMutate, so a failure leaves the bell
  // unchanged with no clue anything went wrong — the toast is the only feedback.
  // onSettled still refetches, so the cache reconciles to the server's truth.
  const markRead = useMutation({
    mutationFn: (id: number) => api.post<void>(`/notifications/${id}/read`),
    onError: () => pushErrorToast("Couldn't mark that as read. Try again."),
    onSettled: invalidate,
  });
  const markAllRead = useMutation({
    mutationFn: () => api.post<void>("/notifications/read-all"),
    onError: () => pushErrorToast("Couldn't mark all as read. Try again."),
    onSettled: invalidate,
  });
  const dismiss = useMutation({
    mutationFn: (id: number) => api.post<void>(`/notifications/${id}/dismiss`),
    onError: () => pushErrorToast("Couldn't dismiss that notification. Try again."),
    onSettled: invalidate,
  });

  return { list, unreadCount, markRead, markAllRead, dismiss };
}
