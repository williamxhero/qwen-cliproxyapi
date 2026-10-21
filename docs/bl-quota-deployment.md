# bl quota deployment record

SPEC: [williamxhero/qwen-cliproxyapi #1](https://github.com/williamxhero/qwen-cliproxyapi/issues/1).
Verification date: **2026-10-14**. This is a partial delivery record, **not evidence of a completed production deployment**.

## Implemented and checked

- Added `--source bl` to the standalone quota CLI through its existing `internal/bailianquota` implementation. No CPA core or panel code changed; no new Go dependency.
- Executable precedence: `--bl-path`, `BAILIAN_BL_PATH`, then PATH lookup of `bl`. Supports Windows official npm `.cmd` shims, including paths containing spaces.
- Maps only the official monthly percentage and millisecond reset timestamp; unsupported plan/status/period/metrics fields stay absent in live output and cache-only output.
- All unsuccessful queries use the existing error JSON channel without readings. Raw CLI diagnostics and credentials are suppressed.
- Existing browser/cookie cache write/read/age semantics are unchanged. A failed refresh preserves the last cache; stale cache-only reads still fail as `stale_cache`. A bl cache must contain a window: an unsupported metrics-only bl cache fails as `invalid_cache` rather than becoming a successful empty output. Independent review found this edge case; its regression test was confirmed failing before the fix.
- Tests use injected commands and synthetic subprocesses, never real bl, console credentials, or network calls. Mapping, missing/null/wrong-type/range fields, error codes, stderr authentication errors, executable failures, cancellation/timeout, Windows shim launching, and cache boundaries are covered.
- Baseline quota tests passed. The initial new regression test failed with `invalid_arguments` before implementation, and passes with `bl_unavailable` afterward.
- Windows verification environment: actual `go version` is **go1.27.0 windows/amd64** (the worker prompt said 1.26.7). `go build ./...`, `go test ./...`, and `go vet ./...` passed. Quota-package coverage is **95.9%**; `go test -race ./internal/bailianquota -count=1` also passed.
- Linux/amd64 CLI and quota test executable were cross-compiled with `CGO_ENABLED=0`. The complete synthetic quota test executable passed under local WSL Ubuntu. This validates the Unix command runner, not a Linux native-plugin deployment or live panel.

## Actual production facts (read-only SSH inspection)

User instructions identify **yosef-server** as the live CPA runtime. No local Windows instance was substituted for production.

- OS/architecture: Linux x86_64; SSH user: `yosef`.
- CPA services are active **user units**: `cpa-cliproxyapi.service`, `cpa-manager.service`.
- Inspected only non-secret facts from `/data/cpa/CLIProxyAPI/config.yaml`: `plugins.enabled` is true, but `plugins.configs.qwen-cliproxyapi` is **absent**.
- `command -v bl` returned no path in the SSH user's environment. This does not prove absence under every other identity/service PATH.
- No service was stopped/restarted, no production file/cache/profile was modified, and no management secret or console token was displayed.

The SPEC's Windows `QwenQuotaRefresh`/`will`/`C:\ProgramData` deployment is therefore not a verified description of this runtime. There is no configured live Qwen command whose `--source bsk` can simply be replaced. Introducing a native plugin, arranging its Linux ABI/build, installing official bl/Node, or transferring authentication would go beyond a source-only configuration switch and require the deployment-owning project's execution context.

## Required console renewal procedure

For the **Windows installation specified by issue #1**, install official bl and its Node runtime under a stable independent path (example: `C:\ProgramData\cpa-bailian\bl.cmd`), not Hermes's versioned tools directory. The launcher must be complete, not a copied shim without its package/runtime. Run `QwenQuotaRefresh` as **will**, whose console profile is `C:\Users\will\.bailian\config.json`.

When the CLI reports `not_logged_in`:

1. As **will**, run `C:\ProgramData\cpa-bailian\bl.cmd auth login --console` and complete the one-time browser interaction. API-key login cannot read Token Plan quota.
2. Run the rebuilt quota CLI with `--source bl --bl-path <stable path> --check`.
3. Run it again with the same source/path and `--cache-file C:\ProgramData\cpa-qwen-quota\qwen-quota.json`.
4. Verify the scheduled task uses that user and absolute path. Preserve the plugin's `--cache-only --cache-file ... --max-age 45m` arguments, changing only its source selector to `--source bl`.
5. Confirm the cache reports a new `observedAt`, `source: bl`, and exactly the CLI-reported monthly value/reset. Confirm the authorized panel renders these two fields and treats other fields as missing.

A permanently open browser is not needed after login, but **console renewal itself has no supported headless substitute**. Never put profile tokens in config, logs, PRs, or this repository. Cache-only has no console authentication requirement. A stopped/failed refresh eventually surfaces as `stale_cache`, not a fabricated current reading.

For the Windows interactive task, the login-notification wrapper opens the official console once per continuous expired-login episode and clears its suppression marker after a successful cache refresh. It does not run from the LocalCPA service process and never fills credentials. The 10-minute task cadence retries normally after the user signs in.

For **Linux/yosef-server**, first establish the actual quota-refresh identity and that identity's protected `~/.bailian/config.json`; do not reuse Windows paths or presume a `will` user. The same `bl auth login --console` renewal principle applies, using the stable Linux executable under that identity.

## Remaining acceptance work

- **Isolated browser/core end-to-end (criterion 6): not performed.** Passing plugin tests are regression checks, not a live panel acceptance claim. Existing panel code/tests handle missing optional fields, but browser rendering with this built CLI is still unverified.
- **Production deployment and read-only panel numeric verification (criterion 7): not performed.** First resolve the missing live Qwen plugin configuration and the Linux-vs-Windows deployment mismatch in the project that owns the server deployment.
- No other project was edited. No matching deployment-owner session was available for a safe cross-project implementation handoff. Resume in the deployment-owning project with explicit target identity, artifact provenance, stable bl install path, protected console profile, isolated E2E harness, and approved service-change procedure.
- This record includes the console renewal steps; it does **not** attest that anyone installed bl, renewed a token, changed source arguments, or verified live readings.
