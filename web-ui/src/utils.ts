// formatSize renders a raw byte count as a plain, consumer-facing size ("1.5 GB"
// — the molma audience expects GB/MB, never the technical GiB/MiB the live-
// resources view uses). Binary math (1 GB = 1024³) so it round-trips the binary
// units manifests author estimated_size in (APP_MANIFEST.md # Storage) and
// matches the Synology/Windows convention of GB labels on 1024-based math. The
// figure is advisory (APP_STORE.md # Trust model); call sites add the "~"/"about"
// hedge in their own wording.
export function formatSize(bytes: number): string {
  const gb = 1024 ** 3;
  const mb = 1024 ** 2;
  const kb = 1024;
  if (bytes >= gb) return `${(bytes / gb).toFixed(1)} GB`;
  if (bytes >= mb) return `${Math.round(bytes / mb)} MB`;
  if (bytes >= kb) return `${Math.round(bytes / kb)} KB`;
  return `${bytes} B`;
}

export function relativeTime(ms: number): string {
  const s = Math.max(0, Math.round((Date.now() - ms) / 1000));
  if (s < 60) return "just now";
  const m = Math.round(s / 60);
  if (m < 60) return `${m}m ago`;
  const h = Math.round(m / 60);
  if (h < 24) return `${h}h ago`;
  return `${Math.round(h / 24)}d ago`;
}
