# Fix the `access` e2e cloud-boot assertion for the per-cookie strip

- **Status:** done
- **Date:** 2026-07-16
- **Specs touched:** none (test-lane fix only; no spec drift — `ENVIRONMENT.md`/`DECISIONS.md` already state the per-cookie strip, [hosted-cookie-strip-per-cookie.md](hosted-cookie-strip-per-cookie.md))

## What was done

Follow-up to [hosted-cookie-strip-per-cookie.md](hosted-cookie-strip-per-cookie.md) (#335, PR #336), which narrowed the hosted route strip from the whole `Cookie` header to just `malmo_forward_auth`. That PR's "What's next" named the gap directly: "#308: add the cookie-leak probe for both exposure modes ... to the e2e lane" was not done in #336 itself, and the existing `access` scenario in `dev/cloud/cloud-assertions.sh` still asserted the *old* invariant — no `Cookie` header at all reaching the app upstream. Merging #336 broke the `CI / Cloud image` boot gate: `MALMO_CLOUD_ASSERTIONS: FAIL: access: COOKIE LEAK (restricted) — the app upstream received a Cookie header; the #306 whole-header strip is broken`. The failure is the assertion script being right about the old contract and wrong about the new one — the probe cookie (`probe=leakcheck`) the script itself sends is now *supposed* to reach the app, by design.

`dev/cloud/cloud-assertions.sh` `access` scenario, both the restricted (step 3) and public (step 4b) probes:

- Replaced the blanket `grep -qiE '^Cookie:' ... && fail "... whole-header strip is broken"` with two assertions: `Cookie:.*malmo_forward_auth=` must be **absent** (the leak check, narrowed to the one cookie that must never arrive), and `Cookie:.*probe=leakcheck` must be **present** (the strip must not be removing more than the one named cookie).
- Updated the surrounding comments and the scenario's closing summary lines from "whole-header strip" language to "per-cookie strip".

## How it maps to the specs

No spec change — `ENVIRONMENT.md` # Public-by-default and `DECISIONS.md` (2026-07-16, #335) already describe the per-cookie strip correctly; this PR only had a test-lane assertion that still encoded the superseded contract.

## How it was tested

`bash -n dev/cloud/cloud-assertions.sh` (syntax). The new grep pairs were sanity-checked against representative response strings for both the pass case (`Cookie: probe=leakcheck` only) and the regression case (`Cookie: malmo_forward_auth=SECRET; probe=leakcheck`) — see the two behaviors the assertion must tell apart in the PR discussion. The full QEMU cloud boot (`make test-cloud-qemu` / `CI / Cloud image`) was not re-run locally (needs root + `/dev/kvm` + mkosi per `CLAUDE.md`); verification is the next `CI / Cloud image` run on this branch/PR.

## Known gaps & deviations

- Not run against a real boot locally; relies on the CI job to confirm the corrected assertion passes against the already-merged #336 route behavior.
- #308's broader ask (session-stability login round-trip in the e2e lane) is still open and unrelated to this fix.

## What's next

- Confirm green on `CI / Cloud image` for this branch, then merge.
- #308 session-stability login round-trip remains open, as recorded in #336's progress entry.
