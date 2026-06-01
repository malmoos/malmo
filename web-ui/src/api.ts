// Thin fetch wrapper (WEB_UI.md): prepends /api/v1, sends credentials, parses
// {code,message} errors into a typed ApiError. Shaped to be swappable for
// openapi-fetch when codegen lands.

export class ApiError extends Error {
  code: string;
  status: number;
  constructor(code: string, message: string, status: number) {
    super(message);
    this.code = code;
    this.status = status;
  }
}

// onUnauthenticated is called whenever any request returns 401. The auth
// composable wires this to drop currentUser so the router falls back to the
// login view without each call site having to handle 401 itself.
let onUnauthenticated: (() => void) | null = null;
export function setUnauthenticatedHandler(fn: () => void) {
  onUnauthenticated = fn;
}

async function request<T>(method: string, path: string, body?: unknown): Promise<T> {
  const res = await fetch(`/api/v1${path}`, {
    method,
    credentials: "include",
    headers: body ? { "Content-Type": "application/json" } : undefined,
    body: body ? JSON.stringify(body) : undefined,
  });
  if (!res.ok) {
    const err = await res.json().catch(() => ({}));
    if (res.status === 401 && onUnauthenticated) onUnauthenticated();
    // The brain uses huma's default error model: { detail, title, errors:[{message}] }.
    // Some endpoints (jobs) carry an explicit { code, message }. Accept both, and
    // surface the first per-field message (e.g. the duplicate-install summary).
    const code = err.code ?? err.detail ?? "unknown";
    const message =
      err.message ?? err.errors?.[0]?.message ?? err.detail ?? err.title ?? res.statusText;
    throw new ApiError(code, message, res.status);
  }
  return res.status === 204 ? (undefined as T) : ((await res.json()) as T);
}

export const api = {
  get: <T>(p: string) => request<T>("GET", p),
  post: <T>(p: string, b?: unknown) => request<T>("POST", p, b),
  put: <T>(p: string, b?: unknown) => request<T>("PUT", p, b),
  del: <T>(p: string) => request<T>("DELETE", p),
};

// --- wire types (hand-rolled in v1; generated client is a follow-up) ------

export interface User {
  id: string;
  username: string;
  role: string;
  created_at: number;
  single_user_mode?: boolean;
}

export interface AuthState {
  has_users: boolean;
}

export interface SetupResult {
  user: User;
  recovery_code: string;
}

export interface CatalogEntry {
  id: string;
  name: string;
  version: string;
}

export type Scope = "household" | "personal";

export interface Instance {
  id: string;
  manifest_id: string;
  name: string;
  slug: string;
  version: string;
  state: string;
  url: string;
  owner_user_id: string;
  owner_username: string;
  scope: Scope;
}

// Notification is the bell read surface (NOTIFICATIONS.md # Surfaces). Routing
// fields (audience, variant, user_id) stay server-side; `read` is this caller's
// per-recipient state folded into a bool. `ts` / `resolved_at` are unix epoch ms.
export interface Notification {
  id: number;
  ts: number;
  category: string;
  severity: "info" | "warning" | "error" | "critical";
  summary: string;
  body?: string;
  action_label?: string;
  action_route?: string;
  read: boolean;
  resolved_at?: number;
}

export interface Job {
  job_id: string;
  kind: string;
  status: "running" | "completed" | "failed" | "cancelled" | "cancelling" | "stalled";
  step?: string;
  progress: number;
  result?: Record<string, unknown>;
  error?: { code: string; message: string };
}

// FolderElection is one entry in the install request's config.folders array.
// source is only relevant when the folder's source menu has more than one option.
// subfolder is only included for pick-subfolder folders.
export interface FolderElection {
  folder: string;
  source?: string;
  subfolder?: string;
}

// InstallRequest is the POST /api/v1/apps body for catalog (Door-1) installs.
// scope defaults server-side (household for admins, personal for members).
// confirm:true bypasses the duplicate-install 409 check.
export interface InstallRequest {
  manifest_id: string;
  scope?: Scope;
  confirm?: boolean;
  config: { folders: FolderElection[] };
}

// InstallPlan is the response shape for GET /api/v1/catalog/:id/install-plan
// (Pattern A sync). The brain returns structured fields; the UI owns all wording.
// Advisory only — slice 4 (POST /api/v1/apps with config) is the authoritative path.
export interface SourceMenu {
  options: string[];
  default: string;
}

export interface FolderSources {
  household: SourceMenu;
  personal: SourceMenu;
}

export interface InstallPlanFolder {
  folder: string;
  mode: "read" | "write";
  scope: "whole" | "pick-subfolder";
  subfolder_default?: string;
  sources: FolderSources;
}

export interface InstallPlanPermissions {
  internet: boolean;
  lan: boolean;
  gpu: boolean;
  devices: string[];
  folders: InstallPlanFolder[];
}

export interface InstallPlan {
  manifest_id: string;
  name: string;
  version: string;
  scope_options: Scope[];
  scope_default: Scope;
  permissions: InstallPlanPermissions;
}

// Poll a job to a terminal state (Pattern B). A useJob() composable with
// refetchInterval is the real shape; this is enough for the skeleton.
export async function waitForJob(jobId: string): Promise<Job> {
  for (;;) {
    const job = await api.get<Job>(`/jobs/${jobId}`);
    if (job.status !== "running" && job.status !== "cancelling") return job;
    await new Promise((r) => setTimeout(r, 600));
  }
}
