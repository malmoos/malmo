# Hosted cloud image: ingest the first-boot seed from the real-cloud (Hetzner) metadata channel (#246)

**Status:** done (code-complete + CI-tested fetch logic; real-cloud boot verification is the CL6 live run — see Known gaps)

Closes **#246**, the seed-*channel* half split from #242 (which promoted the first-boot runtime wiring — networking, host-agent, control-plane bundle, seed *service* — into the production `make build-cloud-image` image, see `cloud-image-first-boot-wiring.md`). After #242 a production tenant box boots with the control plane up, but the seed materializer read the provisioning seed **only** from the SMBIOS / systemd-credential channel (the test-lane delivery, and `fw_cfg` clouds). **Hetzner delivers user-data via its metadata service, not SMBIOS**, so a real box came up and `/setup` stayed **503** — never admin-owned, never served at `<box-id>.malmo.network`. This slice adds the real-cloud channel + the first-boot network ordering it needs.

Item 1 of the issue (the cloud-side wire contract) was resolved by onel in the issue thread: the seed is delivered as **raw seed JSON, written verbatim** into the Hetzner user-data field — the exact bytes the brain's `ReadSeed` expects, no `#cloud-config` / YAML / base64 wrapper. The two channels are byte-symmetric; nothing to unwrap on either side.

## What was done

Tooling-only (`dev/cloud/`); plus one test-only Go package. No brain/UI/protocol change.

- **`dev/cloud/malmo-seed-materialize.sh`** — the SMBIOS path is unchanged and tried **first**: present → write `0600 root:root` and exit, **never touching the network** (keeps the air-gapped lane fast; no network ordering needed there). Only when the credential is **absent** does it fall back to the real-cloud channel:
  - `fetch_seed` fetches the seed over HTTP from `MALMO_SEED_METADATA_URL` (default `http://169.254.169.254/hetzner/v1/userdata`) and writes the body **verbatim** to `/var/lib/malmo/seed.json` via the same atomic `install` the SMBIOS path uses.
  - `http_get` does one GET over **`bash /dev/tcp`** — there is **no `curl`/`wget` in the lean image** (`dev/cloud/expected-packages.txt`; `cloud-assertions.sh` already uses this idiom). `timeout(1)` bounds a hung connect; `Connection: close` lets `cat` read the whole response. Returns the body on **200**, signals a clean **404** as a definitive "no seed", and treats any other status / connect failure as **transient → retry**.
  - `fetch_seed` wraps `http_get` in a **bounded retry loop** (`MALMO_SEED_FETCH_DEADLINE`, default 60s; per-attempt `MALMO_SEED_FETCH_TIMEOUT`, inter-attempt `MALMO_SEED_FETCH_INTERVAL`) to ride out the first-boot DHCP race. It **never blocks forever**: a 404 returns immediately, and an unreachable endpoint gives up at the deadline. On no-seed it logs one line and exits 0 — identical to the prior un-seeded behavior, so `/setup` stays 503.
  - The script self-guards `main` behind `[ "${BASH_SOURCE[0]}" = "${0}" ]` so the functions can be sourced and unit-tested without running the materialize flow.
- **`dev/cloud/malmo-seed.service`** — adds a **passive** `After=network.target` (an ordering point that does **not** wait for connectivity; `systemd-networkd-wait-online` stays disabled per #242). The in-script retry does the real waiting; the SMBIOS lane short-circuits before any network access, so this ordering never makes it wait. `Before=host-agent.service` is unchanged, so the bounded retry naturally delays the brain until the seed lands.
- **`dev/cloud/seed_materialize_test.go`** (new, test-only package `cloud`) — runs in `make check` / `ci-go` on every PR. Sources the script and exercises `parse_url`, `fetch_seed` (200 → body verbatim; clean 404 → fast no-seed; unreachable endpoint → bounded give-up), and `http_get` (a 500 is transient, not definitive) against a Go `httptest` mock metadata server. Skips on non-Linux / when `bash`/`timeout` are absent. This covers the genuinely novel shell logic in CI so the CL6 live boot only has to prove the real endpoint + DHCP timing, not the parsing/retry — onel's recommended "CI-testable slice."
- **`dev/cloud/stage-control-plane.sh`** — the staging comment that called the real-cloud channel "the #242 follow-up" now describes it as the SMBIOS-first / metadata-fallback behavior (#246). No staging-logic change — the script + unit are already staged into the wiring tree by #242.

## How it maps to the specs

- `ENVIRONMENT.md` # Provisioning & first-boot — the "Production image first-boot wiring (realized, #242)" bullet no longer calls #246 "open"; a new **"Real-cloud seed channel (realized, #246)"** bullet under # Admin bootstrap — as built records the SMBIOS-first short-circuit, the `/dev/tcp` metadata fallback, the bounded retry + passive `After=network.target` ordering, the CI test, and the #251 residual-risk pointer.

## Decisions

- **Metadata-endpoint secret persistence → accept + follow-up (#251), not a firewall rule here.** Hetzner keeps the user-data (admin-bootstrap secret + acme-dns password) retrievable for the server's life, reachable by any local process / app container — a cloud-metadata SSRF exposure. It is **not created by #246** (the endpoint exists the moment the box boots on Hetzner regardless), and the mitigation is a **standing egress policy** owned by host-agent's hosted firewall posture (`wiring_hosted.go` lists nftables LAN-scoping as deliberately not-yet-wired in hosted; the image applies no malmo nftables policy today). Bolting a one-shot block into a first-boot shell script would be the wrong owner and would be clobbered by the future firewall reconciler — and would add unverifiable-until-CL6 surface to the critical path. Filed as **#251** (assignee onel).
- **`/dev/tcp`, not `curl`.** onel's design sketch used `curl --fail`; the lean image has neither `curl` nor `wget` (`expected-packages.txt`). Used the existing `bash /dev/tcp` idiom instead.
- **Endpoint as an overridable URL.** `MALMO_SEED_METADATA_URL` keeps the GCP/other-cloud swap (different path + a `Metadata-Flavor` header) a later config change, not a rewrite, and lets the CI test point at a local mock.

## Known gaps & deviations

- **No local build/boot verification.** mkosi 26 on Ubuntu 24.04 hits the `PR_CAPBSET_DROP` EPERM blocker (#189), so `make build-cloud-image` / the QEMU cloud lane cannot run on the maintainer box. The novel shell logic is CI-covered by the Go test against a mock; the real Hetzner endpoint + DHCP timing is the **cloud#6 / CL6 live run** — the same deferral #242/#206/#208 took. The shared root-owned `install` write and the SMBIOS short-circuit are unchanged from #220 and stay covered by the QEMU cloud lane.
- **Un-seeded *air-gapped* boot pays the retry deadline.** Boot 1 of the manual QEMU cloud lane (`run-cloud-tests.sh`, no SMBIOS credential) now enters the metadata fallback and waits the full `MALMO_SEED_FETCH_DEADLINE` (default 60s, within the lane's 360s/boot budget) before reporting no-seed, because an air-gapped box cannot distinguish "seed coming once DHCP settles" from "no metadata ever." On a **real** cloud the un-seeded case exits fast on the 404; only a box with no reachable metadata endpoint at all waits the deadline. Not a regression to the seeded boots (they short-circuit on the SMBIOS credential).
- **HTTP body trailing-newline.** The `$(...)` capture strips a trailing newline from the fetched body; the SMBIOS path preserves bytes exactly. Harmless — `ReadSeed` is `json.Unmarshal`, which ignores trailing whitespace — and noted rather than worked around.
- **Residual metadata-secret exposure (#251).** See Decisions.

## What's next

- **#251** — block app-container / non-root egress to `169.254.169.254` as part of host-agent's hosted firewall posture.
- **cloud#6 / CL6 live run** — boot a real seeded Hetzner VM from a `make build-cloud-image` image whose user-data carries the raw seed JSON: confirm the metadata channel ingests it, `/setup` accepts the bootstrap secret, and `<box-id>.malmo.network` is served over HTTPS. Record the result.
- **Cloud repo (cloud#3)** — the provisioning side must place the raw seed JSON verbatim in the Hetzner user-data field (no wrapper), matching the byte contract the brain's `ReadSeed` already defines.
