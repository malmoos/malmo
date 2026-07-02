<script setup lang="ts">
// LiveResources is the fourth locked top-bar element (DASHBOARD.md # the top
// bar; LOCAL_ANALYTICS.md # Real-time system resources): a chevron next to the
// avatar menu that opens a compact panel of live CPU / RAM / network / disk-IO
// gauges. Available to every signed-in user — host-level state isn't per-user
// data.
//
// Opening the panel opens the SSE stream; closing it closes the stream. That is
// the UI half of the "only while watching, zero idle cost" contract: the brain
// ref-counts subscribers and stops polling host-agent when the last disconnects
// (BRAIN_UI_PROTOCOL.md:177). The first frame after a (re)connect carries null
// rate fields (no prior sample to diff) — we render those as "—" until the
// second sample arrives ~1s later (BRAIN_UI_PROTOCOL.md:179).
import { ref, computed, watch, onMounted, onUnmounted } from "vue";
import { Activity, ChevronDown } from "lucide-vue-next";
import { api, type SystemStorage, type DiskSpace } from "./api";

interface NetRate {
  iface: string;
  rx_bps: number | null;
  tx_bps: number | null;
}
interface DiskRate {
  dev: string;
  read_bps: number | null;
  write_bps: number | null;
}
interface Sample {
  cpu_pct: number | null;
  load: [number, number, number];
  mem: { used_bytes: number; total_bytes: number; available_bytes: number };
  net: NetRate[];
  disk: DiskRate[];
  uptime_s: number;
}

const open = ref(false);
const root = ref<HTMLElement | null>(null);
const sample = ref<Sample | null>(null);
// Disk fullness is a separate concern from the live gauges: it doesn't change at
// the 1 Hz cadence of CPU/RAM, so it's a one-time poll on panel open (mirroring
// how the install-plan dialog reads free bytes), not part of the SSE stream.
const storage = ref<SystemStorage | null>(null);
const storageOpen = ref(false); // per-disk detail collapsed by default

let es: EventSource | null = null;

async function connect() {
  if (es) return;
  // EventSource bypasses the api.ts fetch wrapper; write the full path and pass
  // the session cookie (withCredentials), exactly as useEvents.ts does.
  es = new EventSource("/api/v1/system/live", { withCredentials: true });
  es.addEventListener("sample", (e) => {
    sample.value = JSON.parse((e as MessageEvent).data) as Sample;
  });
  // One-time Storage poll. Guard the assignment on es so a fetch that resolves
  // after the panel closed (disconnect cleared es) doesn't leave stale bars.
  try {
    const s = await api.get<SystemStorage>("/system/storage");
    if (es) storage.value = s;
  } catch {
    // Leave the Storage section hidden on a fetch error rather than showing
    // a misleading empty disk — same posture as the brain's fail-open zeros.
  }
}

function disconnect() {
  es?.close();
  es = null;
  sample.value = null; // forget the last frame so reopening starts clean
  storage.value = null;
  storageOpen.value = false;
}

function toggle() {
  open.value = !open.value;
}

watch(open, (isOpen) => (isOpen ? connect() : disconnect()));
// Safety net: if the component unmounts while the panel is open, close the
// stream so the brain's poller ref-count drops.
onUnmounted(disconnect);

// Click-outside closes the panel (and, via the watcher, the stream).
function onDocClick(e: MouseEvent) {
  if (open.value && root.value && !root.value.contains(e.target as Node)) {
    open.value = false;
  }
}
onMounted(() => document.addEventListener("click", onDocClick));
onUnmounted(() => document.removeEventListener("click", onDocClick));

// --- formatting: wire units are SI bytes; the UI humanizes (LOCAL_ANALYTICS) ---

function humanBytes(b: number): string {
  const tib = 1024 ** 4;
  const gib = 1024 ** 3;
  const mib = 1024 ** 2;
  if (b >= tib) return `${(b / tib).toFixed(1)} TiB`;
  if (b >= gib) return `${(b / gib).toFixed(1)} GiB`;
  if (b >= mib) return `${(b / mib).toFixed(0)} MiB`;
  return `${(b / 1024).toFixed(0)} KiB`;
}

function humanRate(bps: number | null): string {
  if (bps == null) return "—";
  if (bps >= 1024 * 1024) return `${(bps / 1024 / 1024).toFixed(1)} MB/s`;
  if (bps >= 1024) return `${(bps / 1024).toFixed(0)} KB/s`;
  return `${bps} B/s`;
}

function pct(v: number | null): string {
  return v == null ? "—" : `${Math.round(v)}%`;
}

function humanUptime(s: number): string {
  const d = Math.floor(s / 86400);
  const h = Math.floor((s % 86400) / 3600);
  const m = Math.floor((s % 3600) / 60);
  if (d > 0) return `${d}d ${h}h`;
  if (h > 0) return `${h}h ${m}m`;
  return `${m}m`;
}

// Hoisted out of the template: an inline `.map` closure over `sample.load`
// defeats the `v-else` null-narrowing of `sample` (vue-tsc flags it), and
// `noUncheckedIndexedAccess` makes the tuple index possibly-undefined. The
// early null guard here narrows cleanly and iterating `s.load` drops the index.
const loadLine = computed(() => {
  const s = sample.value;
  if (!s) return "";
  const labels = ["1m", "5m", "15m"];
  return s.load.map((v, i) => `${labels[i] ?? ""} ${v.toFixed(2)}`).join("  ");
});

const cpuBar = computed(() => (sample.value?.cpu_pct == null ? 0 : Math.min(100, sample.value.cpu_pct)));
const memBar = computed(() => {
  const m = sample.value?.mem;
  if (!m || m.total_bytes === 0) return 0;
  return Math.min(100, (m.used_bytes / m.total_bytes) * 100);
});

// Storage: the collapsed bar is the aggregate across every reported volume
// (used = Σtotal − Σfree); expanding shows one bar per disk.
const disks = computed<DiskSpace[]>(() => storage.value?.disks ?? []);
const storageTotal = computed(() => disks.value.reduce((s, d) => s + d.total_bytes, 0));
const storageUsed = computed(() => storageTotal.value - disks.value.reduce((s, d) => s + d.free_bytes, 0));
const storageBar = computed(() => (storageTotal.value === 0 ? 0 : Math.min(100, (storageUsed.value / storageTotal.value) * 100)));
function diskBar(d: DiskSpace): number {
  return d.total_bytes === 0 ? 0 : Math.min(100, ((d.total_bytes - d.free_bytes) / d.total_bytes) * 100);
}

</script>

<template>
  <div ref="root" class="live-wrap">
    <button class="live-btn" :class="{ active: open }" aria-label="System resources" @click="toggle">
      <Activity class="size-[18px]" />
    </button>

    <div v-if="open" class="panel">
      <div class="panel-head"><strong>System</strong></div>

      <p v-if="!sample" class="empty">Connecting…</p>

      <template v-else>
        <div class="metric">
          <div class="metric-row">
            <span class="label">CPU</span>
            <span class="value">{{ pct(sample.cpu_pct) }}</span>
          </div>
          <div class="bar"><div class="bar-fill" :style="{ width: cpuBar + '%' }"></div></div>
        </div>

        <div class="metric">
          <div class="metric-row">
            <span class="label">Memory</span>
            <span class="value">{{ humanBytes(sample.mem.used_bytes) }} / {{ humanBytes(sample.mem.total_bytes) }}</span>
          </div>
          <div class="bar"><div class="bar-fill" :style="{ width: memBar + '%' }"></div></div>
        </div>

        <div v-if="disks.length" class="metric">
          <button class="storage-head" :aria-expanded="storageOpen" @click.stop="storageOpen = !storageOpen">
            <span class="label">Storage</span>
            <span class="value">{{ humanBytes(storageUsed) }} / {{ humanBytes(storageTotal) }}</span>
            <ChevronDown class="chev" :class="{ open: storageOpen }" />
          </button>
          <div class="bar"><div class="bar-fill" :style="{ width: storageBar + '%' }"></div></div>

          <div v-if="storageOpen" class="storage-detail">
            <div v-for="d in disks" :key="d.label" class="metric">
              <div class="metric-row">
                <span class="label">{{ d.label }}</span>
                <span class="value">{{ humanBytes(d.free_bytes) }} free of {{ humanBytes(d.total_bytes) }}</span>
              </div>
              <div class="bar"><div class="bar-fill" :style="{ width: diskBar(d) + '%' }"></div></div>
            </div>
          </div>
        </div>

        <div v-if="sample.net.length" class="group">
          <div class="group-label">Network</div>
          <div v-for="n in sample.net" :key="n.iface" class="io-row">
            <span class="label">{{ n.iface }}</span>
            <span class="io">↓ {{ humanRate(n.rx_bps) }}</span>
            <span class="io">↑ {{ humanRate(n.tx_bps) }}</span>
          </div>
        </div>

        <div v-if="sample.disk.length" class="group">
          <div class="group-label">Disk</div>
          <div v-for="d in sample.disk" :key="d.dev" class="io-row">
            <span class="label">{{ d.dev }}</span>
            <span class="io">R {{ humanRate(d.read_bps) }}</span>
            <span class="io">W {{ humanRate(d.write_bps) }}</span>
          </div>
        </div>

        <div class="foot">
          <span>load {{ loadLine }}</span>
          <span>up {{ humanUptime(sample.uptime_s) }}</span>
        </div>
      </template>
    </div>
  </div>
</template>

<style scoped>
.live-wrap { position: relative; }
.live-btn {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 2rem;
  height: 2rem;
  padding: 0;
  color: var(--color-muted-foreground);
  border: 1px solid var(--color-border);
  background: var(--color-card);
  border-radius: var(--radius);
  cursor: pointer;
}
.live-btn:hover { background: var(--color-muted); }
.live-btn.active { background: var(--color-muted); border-color: var(--color-olive-300); }

.panel {
  position: absolute;
  top: calc(100% + 8px);
  right: 0;
  width: 280px;
  background: var(--color-card);
  border: 1px solid var(--color-border);
  border-radius: var(--radius);
  box-shadow: 0 8px 28px rgba(0, 0, 0, 0.12);
  z-index: 50;
  padding: 0.5rem 0.85rem 0.7rem;
}
.panel-head { padding: 0.3rem 0 0.5rem; }
.panel-head strong { font-size: 0.85rem; }
.empty { color: var(--color-muted-foreground); font-size: 0.85rem; text-align: center; padding: 1rem; margin: 0; }

.metric { margin-bottom: 0.6rem; }
.metric-row { display: flex; justify-content: space-between; align-items: baseline; }
.label { font-size: 0.78rem; color: var(--color-muted-foreground); }
.value { font-size: 0.8rem; color: var(--color-foreground); font-variant-numeric: tabular-nums; }
.bar {
  margin-top: 4px;
  height: 5px;
  border-radius: 999px;
  background: var(--color-muted);
  overflow: hidden;
}
.bar-fill {
  height: 100%;
  background: var(--color-accent);
  border-radius: 999px;
  transition: width 0.4s ease;
}

.storage-head {
  display: flex;
  align-items: baseline;
  gap: 0.4rem;
  width: 100%;
  padding: 0;
  background: none;
  border: none;
  cursor: pointer;
  text-align: left;
}
.storage-head .value { margin-left: auto; }
.storage-head .chev {
  width: 14px;
  height: 14px;
  color: var(--color-muted-foreground);
  align-self: center;
  transition: transform 0.2s ease;
}
.storage-head .chev.open { transform: rotate(180deg); }
.storage-detail { margin-top: 0.4rem; padding-left: 0.5rem; }
.storage-detail .metric:last-child { margin-bottom: 0; }

.group { margin-top: 0.5rem; }
.group-label {
  font-size: 0.65rem;
  text-transform: uppercase;
  letter-spacing: 0.05em;
  color: var(--color-muted-foreground);
  margin-bottom: 0.2rem;
}
.io-row {
  display: flex;
  align-items: baseline;
  gap: 0.5rem;
  font-size: 0.76rem;
  color: var(--color-foreground);
  padding: 0.12rem 0;
}
.io-row .label { flex: 1; min-width: 0; color: var(--color-muted-foreground); }
.io { font-variant-numeric: tabular-nums; color: var(--color-foreground); }

.foot {
  display: flex;
  justify-content: space-between;
  margin-top: 0.6rem;
  padding-top: 0.45rem;
  border-top: 1px solid var(--color-border);
  font-size: 0.72rem;
  color: var(--color-muted-foreground);
  font-variant-numeric: tabular-nums;
}
</style>
