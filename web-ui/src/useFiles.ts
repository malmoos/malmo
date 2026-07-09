// File-manager API layer (FILES.md). Metadata ops go through the JSON api.ts
// wrapper; content transfer (download/upload) bypasses it — the wrapper always
// JSON-encodes bodies and parses JSON responses, which cannot carry a streamed
// File body or a binary download. Download is a same-origin <a download> hit
// (cookie rides along); upload is an XHR PUT so the browser reports progress
// (fetch has no upload-progress event).
import { api, ApiError, type FileEntry, type FileLocation } from "@/api";

export type FileRoot = "home" | "shared";

export interface FileListing {
  entries: FileEntry[];
}

export function listFiles(root: FileRoot, path: string): Promise<FileListing> {
  return api.post<FileListing>("/files/list", { root, path });
}

export function makeFolder(root: FileRoot, path: string): Promise<void> {
  return api.post<void>("/files/mkdir", { root, path });
}

export function deleteEntry(root: FileRoot, path: string): Promise<void> {
  return api.post<void>("/files/delete", { root, path });
}

export function moveEntry(from: FileLocation, to: FileLocation): Promise<void> {
  return api.post<void>("/files/move", { from, to });
}

export function copyEntry(from: FileLocation, to: FileLocation): Promise<void> {
  return api.post<void>("/files/copy", { from, to });
}

// joinPath appends a segment to a relative path, keeping "" for a root listing.
export function joinPath(path: string, name: string): string {
  return path ? `${path}/${name}` : name;
}

// parentPath returns the path one level up ("" at a root).
export function parentPath(path: string): string {
  const i = path.lastIndexOf("/");
  return i === -1 ? "" : path.slice(0, i);
}

// downloadURL is the same-origin content endpoint. An <a download> GETs it with
// the session cookie; the brain's Content-Disposition names the saved file.
export function downloadURL(root: FileRoot, path: string): string {
  const q = new URLSearchParams({ root, path });
  return `/api/v1/files/content?${q.toString()}`;
}

// uploadFile streams a File to the content endpoint via XHR, reporting progress.
// Resolves on 2xx; rejects with an ApiError carrying the brain's {code,message}.
export function uploadFile(
  root: FileRoot,
  path: string,
  file: File,
  onProgress?: (pct: number) => void,
): Promise<void> {
  return new Promise((resolve, reject) => {
    const xhr = new XMLHttpRequest();
    const q = new URLSearchParams({ root, path });
    xhr.open("PUT", `/api/v1/files/content?${q.toString()}`);
    xhr.withCredentials = true;
    if (onProgress) {
      xhr.upload.onprogress = (e) => {
        if (e.lengthComputable) onProgress(Math.round((e.loaded / e.total) * 100));
      };
    }
    xhr.onload = () => {
      if (xhr.status >= 200 && xhr.status < 300) {
        resolve();
        return;
      }
      let code = "upload_failed";
      let message = xhr.statusText || "Upload failed";
      try {
        const body = JSON.parse(xhr.responseText);
        code = body.code ?? code;
        message = body.message ?? message;
      } catch {
        // non-JSON error body; keep the status text
      }
      reject(new ApiError(code, message, xhr.status));
    };
    xhr.onerror = () => reject(new ApiError("network_error", "Upload failed", 0));
    xhr.send(file);
  });
}

// formatBytes renders a human size for the listing (files only; dirs show "—").
export function formatBytes(n: number): string {
  if (n < 1024) return `${n} B`;
  const units = ["KB", "MB", "GB", "TB"];
  let size = n / 1024;
  let i = 0;
  while (size >= 1024 && i < units.length - 1) {
    size /= 1024;
    i++;
  }
  return `${size.toFixed(size < 10 ? 1 : 0)} ${units[i]}`;
}

// sortEntries orders a listing folders-first, then case-insensitive by name —
// the conventional file-manager order.
export function sortEntries(entries: FileEntry[]): FileEntry[] {
  return [...entries].sort((a, b) => {
    if (a.dir !== b.dir) return a.dir ? -1 : 1;
    return a.name.localeCompare(b.name, undefined, { sensitivity: "base" });
  });
}
