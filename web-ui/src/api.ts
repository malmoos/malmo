// Thin fetch wrapper (WEB_UI.md): prepends /api/v1, sends credentials, parses
// {code,message} errors into a typed ApiError. Shaped to be swappable for
// openapi-fetch when codegen lands.

export class ApiError extends Error {
  code: string;
  constructor(code: string, message: string) {
    super(message);
    this.code = code;
  }
}

async function request<T>(method: string, path: string, body?: unknown): Promise<T> {
  const res = await fetch(`/api/v1${path}`, {
    method,
    credentials: "include",
    headers: body ? { "Content-Type": "application/json" } : undefined,
    body: body ? JSON.stringify(body) : undefined,
  });
  if (!res.ok) {
    const err = await res.json().catch(() => ({ code: "unknown", message: res.statusText }));
    throw new ApiError(err.code ?? "unknown", err.message ?? res.statusText);
  }
  return res.status === 204 ? (undefined as T) : ((await res.json()) as T);
}

export const api = {
  get: <T>(p: string) => request<T>("GET", p),
  post: <T>(p: string, b?: unknown) => request<T>("POST", p, b),
  del: <T>(p: string) => request<T>("DELETE", p),
};

// --- wire types (hand-rolled in v1; generated client is a follow-up) ------

export interface CatalogEntry {
  id: string;
  name: string;
  version: string;
}

export interface Instance {
  id: string;
  manifest_id: string;
  name: string;
  slug: string;
  version: string;
  state: string;
  url: string;
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

// Poll a job to a terminal state (Pattern B). A useJob() composable with
// refetchInterval is the real shape; this is enough for the skeleton.
export async function waitForJob(jobId: string): Promise<Job> {
  for (;;) {
    const job = await api.get<Job>(`/jobs/${jobId}`);
    if (job.status !== "running" && job.status !== "cancelling") return job;
    await new Promise((r) => setTimeout(r, 600));
  }
}
