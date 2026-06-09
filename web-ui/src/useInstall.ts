// useInstall — the catalog-app install flow for a single app, extracted from
// StoreView so the app detail page (where Install now lives) owns it without
// duplicating ~150 lines (DASHBOARD.md # install authorization).
//
// It owns: the advisory install-plan fetch, the consent-dialog open/close state,
// the install mutation with its 409-duplicate / 422-election / mid-job error
// branches, and the per-app button logic (does the caller already have a
// household or own-personal instance?). The view renders InstallDialog from
// `activePlan` and wires the returned handlers; all wording stays in the view.
import { computed, ref, watch, type Ref } from "vue";
import { useQuery, useMutation, useQueryClient } from "@tanstack/vue-query";
import { useAuth } from "./auth";
import {
  api,
  waitForJob,
  ApiError,
  type Instance,
  type Job,
  type InstallPlan,
  type InstallRequest,
  type Scope,
} from "./api";

export function useInstall(manifestId: Ref<string>) {
  const qc = useQueryClient();
  const { currentUser, singleUserMode } = useAuth();

  const apps = useQuery({
    queryKey: ["apps"],
    queryFn: () => api.get<{ apps: Instance[] }>("/apps"),
  });

  // ── Install-plan dialog state ────────────────────────────────────────────────
  // planOpen drives the install-plan fetch + dialog visibility. dialogScope is the
  // scope (personal/household) the caller chose before opening.
  const planOpen = ref(false);
  const dialogScope = ref<Scope>("personal");
  const dialogError = ref<string | null>(null); // 422 election rejection (inline)
  const duplicateInfo = ref<string | null>(null); // 409 duplicate-install (banner)
  const pendingRequest = ref<InstallRequest | null>(null); // last sent, for confirm retry
  const installingId = ref<string | null>(null); // set once POST is accepted (202)
  const installError = ref<string | null>(null); // failure during the job, after dialog closed

  const installPlanQuery = useQuery({
    queryKey: computed(() => ["install-plan", manifestId.value]),
    queryFn: () => api.get<InstallPlan>(`/catalog/${manifestId.value}/install-plan`),
    enabled: planOpen,
    staleTime: 0,
    // The plan for a given (id, role) is stable for the life of a dialog; a
    // background refetch would swap the plan out from under an open dialog.
    refetchOnWindowFocus: false,
  });

  // activePlan is derived (not mirrored via a queryFn side-effect) so a background
  // refetch can't mutate dialog state — null until the dialog is open and resolved.
  const activePlan = computed<InstallPlan | null>(() =>
    planOpen.value ? (installPlanQuery.data.value ?? null) : null,
  );

  function openInstallDialog(scope: Scope = "personal") {
    dialogError.value = null;
    duplicateInfo.value = null;
    pendingRequest.value = null;
    dialogScope.value = scope;
    planOpen.value = true;
  }

  function closeDialog() {
    planOpen.value = false;
    dialogError.value = null;
    duplicateInfo.value = null;
    pendingRequest.value = null;
  }

  // ── Install mutation ──────────────────────────────────────────────────────────

  const install = useMutation({
    mutationFn: async (req: InstallRequest) => {
      const job = await api.post<Job>("/apps", req);
      // POST accepted (202) → job running. 409 duplicate / 422 election rejection
      // would have thrown above with the dialog still open. Close it now and mark
      // the app installing.
      installingId.value = req.manifest_id;
      planOpen.value = false;
      const done = await waitForJob(job.job_id);
      if (done.status !== "completed") {
        throw new Error(done.error?.message ?? "The install didn't finish.");
      }
      return done;
    },
    onSuccess: () => {
      closeDialog();
    },
    onError: (err: unknown) => {
      if (err instanceof ApiError && err.code === "duplicate-install") {
        // Thrown at POST time, dialog still open → warn-don't-block banner.
        duplicateInfo.value = err.message;
      } else if (installingId.value) {
        // Failed during the job, after the dialog closed → standalone banner.
        installError.value = (err as Error).message;
      } else {
        // Failed at POST (422 election rejection) → inline in the open dialog.
        dialogError.value = (err as Error).message;
      }
    },
    onSettled: async () => {
      // Keep the app in "Installing…" until the apps list reflects the new
      // instance, so it flips straight to "Open" with no "Install" flicker.
      await qc.invalidateQueries({ queryKey: ["apps"] });
      installingId.value = null;
    },
  });

  function handleSubmit(req: InstallRequest) {
    dialogError.value = null;
    duplicateInfo.value = null;
    installError.value = null;
    pendingRequest.value = req;
    install.mutate(req);
  }

  // AppDetailView's route component is reused across /store/:id navigations (the
  // instance isn't unmounted, only manifestId changes), so this composable's
  // state refs persist. Reset all install-flow display state when the app
  // changes — otherwise a dialog/banner from the previous app leaks onto the new
  // one: a job that fails after you've navigated away would write installError
  // against the wrong page, and a still-true planOpen would pop the consent
  // dialog on an app you never clicked Install on (its plan query re-fetches for
  // the new id). The background mutation itself keeps running; only the view
  // state is cleared.
  watch(manifestId, () => {
    closeDialog();
    installError.value = null;
    installingId.value = null;
  });

  function handleConfirmDuplicate() {
    if (!pendingRequest.value) return;
    const req = { ...pendingRequest.value, confirm: true };
    // Close the plan dialog before clearing duplicateInfo: a 409 left planOpen
    // true (the throw skipped the close in mutationFn), so clearing duplicateInfo
    // alone would re-satisfy the dialog's `activePlan && !duplicateInfo` guard and
    // flash the consent dialog back open until the retry's 202 lands.
    planOpen.value = false;
    duplicateInfo.value = null;
    install.mutate(req);
  }

  // ── Per-app button logic ──────────────────────────────────────────────────────
  // The caller-relevant instances for this app: a shared (household) copy anyone
  // permitted can open, and the caller's own personal copy.
  const householdInstance = computed<Instance | undefined>(() =>
    (apps.data.value?.apps ?? []).find((a) => a.manifest_id === manifestId.value && a.scope === "household"),
  );
  const ownPersonalInstance = computed<Instance | undefined>(() =>
    (apps.data.value?.apps ?? []).find(
      (a) =>
        a.manifest_id === manifestId.value &&
        a.scope === "personal" &&
        a.owner_user_id === currentUser.value?.id,
    ),
  );

  // Admins (outside single-user mode) get the "Install for the whole household"
  // split-button option; everyone else gets a plain Install button.
  const dropdownItems = computed(() =>
    currentUser.value?.role === "admin" && !singleUserMode.value
      ? [{ label: "Install for the whole household", action: () => openInstallDialog("household") }]
      : [],
  );

  const installing = computed(() => installingId.value === manifestId.value);

  return {
    // dialog
    activePlan,
    dialogScope,
    dialogError,
    duplicateInfo,
    installError,
    installing,
    install,
    // handlers
    openInstallDialog,
    closeDialog,
    handleSubmit,
    handleConfirmDuplicate,
    // button logic
    householdInstance,
    ownPersonalInstance,
    dropdownItems,
  };
}
