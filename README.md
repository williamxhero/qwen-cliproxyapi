# Qwen CLIProxyAPI Plugin

A native CLIProxyAPI v8 credential provider for Alibaba Bailian **Token Plan / Coding Plan**, with a standalone console quota CLI.

## Problem

A generic OpenAI-compatible upstream does not make Bailian plan keys first-class management credentials. Furthermore, plan keys cannot read console quota (`ConsoleNeedLogin`). Coding Plan's public `/models` response is not credential validation.

## Solution

Expose one provider, **`qwen`**, using CLIProxyAPI's credential scheduler, rotation and cooldowns. Discover models once, translate client protocols to upstream OpenAI Chat Completions, and invoke `bailian-quota.exe` for honest console-derived quota readings.

## Features

- Each configured key becomes a stable `qwen-key-<sha256>.json` auth file with an optional readable label, visible in the management credential page.
- Execution always uses the **credential selected by the host**, never the catalog's default key.
- OpenAI Chat Completions and Anthropic Messages requests, tool calls, image parts and streaming SSE translation, adapted from the MIT reference.
- Authenticated catalog discovery, optional `qwen/` prefix, last-good snapshot and built-in initial fallback.
- Native quota groups, windows, subscription and metrics from an external CLI; no shell invocation, hard timeout, no invented readings.
- Plugin-registered **Qwen 额度** management page: 套餐/status/expiry/remaining days, per-window progress and reset countdowns, CLI observation time, and per-credential/all refresh buttons. Embedded HTML requires no runtime resource files.
- Standalone stdlib-only Go CLI: already-logged-in browser via `bsk`, or explicitly supplied cookie for service/session-0 environments.

## Requirements

- CLIProxyAPI **v8.0.0+** native plugin ABI.
- Windows AMD64; Go 1.26.7+ and a GCC-compatible C compiler with `CGO_ENABLED=1` for the DLL.
- A Bailian Token Plan or Coding Plan API key. Console quota needs a separately logged-in Alibaba account.
- For browser mode: `C:\Users\will\.local\bin\bsk`, its daemon, browser extension and a connected logged-in browser.

## Build

```powershell
$env:CGO_ENABLED = '1'
go build -buildmode=c-shared -o plugins/windows/amd64/qwen-cliproxyapi.dll .
go build -o bin/bailian-quota.exe ./cmd/bailian-quota
```

Build outputs are ignored by Git. Deploy the DLL under the host plugin directory's `windows/amd64` subdirectory. Deploy the CLI separately and configure its absolute executable path. Do not install into a running host without following its normal deployment procedure.

## Configuration

Set the key in the **host process environment** rather than storing it in this repository:

```yaml
plugins:
  dir: "D:\\WILL\\AGENT\\CPA\\qwen-cliproxyapi\\plugins"
  configs:
    qwen-cliproxyapi:
      base-url: "https://token-plan.cn-beijing.maas.aliyuncs.com/compatible-mode/v1"
      api-keys:
        - value: "${QWEN_API_KEY}"
          name: "Token Plan"
      model-prefix: { enabled: true, value: "qwen" }
      catalog: { refresh-interval: "30m", stale-while-unavailable: true }
      protocols: { chat-completions: true, messages: true }
      request-timeout: "5m"
      max-response-bytes: 67108864
      allow-http: false
      quota-source: command
      command: "D:\\WILL\\AGENT\\CPA\\qwen-cliproxyapi\\bin\\bailian-quota.exe"
      command-args: ["--json"]
      command-timeout: "90s"
      display-name: "阿里云百炼额度（CLI）"
```

### Configuration table

| Option | Default | Meaning |
|---|---|---|
| `api-keys` | Required | Objects containing `value` and optional `name`; `${ENV_VAR}` expansion; empty/duplicate keys rejected. |
| `base-url` | Token Plan CN URL above | HTTPS upstream without userinfo, query or fragment. |
| `model-prefix.enabled` / `.value` | `true` / `qwen` | Publish `qwen/<id>` or bare IDs. |
| `catalog.refresh-interval` | `30m` | Discovery cadence; minimum `1m`. |
| `catalog.stale-while-unavailable` | `true` | Retain last successful catalog on failure. |
| `protocols.chat-completions` | `true` | Allow OpenAI Chat Completions clients. |
| `protocols.messages` | `true` | Allow Anthropic Messages clients, translated to Chat Completions. |
| `request-timeout` | `5m` | Upstream request deadline. |
| `max-response-bytes` | `67108864` | Non-streaming response limit (64 MiB). |
| `allow-http` | `false` | HTTP only for local mocks when explicitly enabled. |
| `quota-source` | `command` | External CLI is the only quota source. |
| `command` | Required | Executable path; **not** a shell command. |
| `command-args` | `["--json"]` | Argument slice; no `cmd /c`, pipes or interpolation. |
| `command-timeout` | `90s` | Hard quota command deadline. |
| `display-name` | `阿里云百炼额度（CLI）` | Quota group display name. |

Without an explicit key name labels are `Qwen 1`, `Qwen 2`, etc. IDs depend on the key hash, not label or ordering. Removed configuration keys are not automatically deleted from the host auth directory: the ABI has no deletion callback. Manage stale credentials explicitly in the host.

For Coding Plan use `https://coding.dashscope.aliyuncs.com/v1` (CN) or `https://coding-intl.dashscope.aliyuncs.com/v1` (international). Coding Plan keys typically start with `sk-sp-`. All execution goes to `{base-url}/chat/completions`; no fallback to another provider or credential is performed by the plugin.

## Model catalogue

The plugin fetches `GET {base-url}/models` using a configured key. Successful discovery replaces the model list; prefixing changes only public IDs, not upstream IDs. Discovery uses the first configured key, while requests use the host-selected credential.

The documented initial fallback is **`qwen3.8-max`, `qwen3.8-flash`, `qwen3.7-max`**. These are route candidates, not proof of key validity, subscription access or remaining quota. With stale policy enabled an outage retains the last successful catalog. Disable stale policy for fail-closed discovery.

Token Plan `/models` is auth-protected. Coding Plan `/models` is a static public list: **never interpret successful discovery as successful credential validation**.

## Quota via CLI

`cmd/bailian-quota` is implemented in this repository, using Go's standard library only.

```powershell
.\bin\bailian-quota.exe --json
.\bin\bailian-quota.exe --pretty --browser <instance-id>
.\bin\bailian-quota.exe --check --timeout 90s
.\bin\bailian-quota.exe --source cookie --cookie-file C:\private\bailian-cookie.txt
```

Flags: `--json` (default normalized JSON), `--pretty`, `--timeout 90s`, `--check` (login/reachability only), `--source bsk|cookie`, `--browser <id>`, `--cookie <header>`, `--cookie-file <path>`, `--verbose`.

Browser mode selects the first connected browser unless `--browser` is supplied, starts a session **with `--no-focus`**, navigates the Bailian console, then runs an asynchronous console RPC evaluation. It never extracts, prints or saves browser cookies/tokens. The session is stopped even on failure. Avoid running it concurrently with another browser driver. Requests remain on Alibaba console origins.

Cookie mode sends the same form-encoded console RPCs with an explicitly supplied cookie header. Prefer a protected external `--cookie-file` to avoid putting cookie secrets in process argv. A Windows service in **session 0** may not reach the user-session bsk daemon; configure `command-args: ["--json", "--source", "cookie", "--cookie-file", "C:\\private\\bailian-cookie.txt"]` as the fallback. Never commit that file. Cookie expiry requires renewed user login.

The frozen stdout contract is:

```json
{"source":"bsk","plan":"Token Plan 个人版 Standard","planStatus":"生效中","observedAt":"...","windows":[{"window":"1month","usedPercent":100,"resetTime":"2026-10-18T00:00:00+08:00"}],"metrics":[],"notes":[]}
```

The CLI exits 0 only with a genuine reading (or a successful `--check`). Failures exit nonzero with `{"error":"<code>","message":"<human text>"}` on stdout. Optional Coding Plan enrichment must not invalidate successful Token Plan quota.

The plugin maps windows to `QuotaBucket` (`remainingFraction = 1 - usedPercent/100`, clamped), preserves reset timestamps, maps metrics to `Summary` and plan to `Subscription.Plan`. No windows and no metrics is an error; subprocess failure, timeout or invalid JSON does not fabricate a balance.

Use `GET /v0/management/quota/providers` to discover support, then `POST /v0/management/quota/fetch` with `{"auth_index":"<credential index>"}` and management authorization. Console quota is **account-scoped**, not derivable from a plan API key: configure a console login corresponding to the credential's account. Multiple keys sharing one CLI login will display that login's account readings.

### Qwen 额度 management page

The panel's generic credential quota card only recognizes a built-in provider list. Open **Qwen 额度** from the panel's plugin/resource menu instead. The plugin registers `/quota`, served as `GET /v0/resource/plugins/qwen-cliproxyapi/quota`; its self-contained page posts to the same-origin `/v0/management/plugins/qwen-cliproxyapi/quota-usage`. The existing `/quota-info` endpoint and native quota API remain available.

Sign into the management panel with **Remember password** enabled, as required by the MIT reference page. The embedded page decodes the host's `cli-proxy-auth` local storage (including `enc::v1::`) at request time and supplies management authorization; it never embeds a management key or sends requests to a third-party origin. If credentials cannot be accessed or the host rejects authorization, it displays the error rather than fake quota.

`quota-usage` returns `{"cards":[...]}`. An empty body or `{}` refreshes all configured Qwen credentials; `{"auth_index":"<host credential index>"}` refreshes one. Indexes come from the host credential list, not the API-key hash; unknown indexes return 404. A CLI failure remains a card with `error` containing the CLI diagnostic and no fabricated reading. The page displays **读不到额度：<错误>** and clears old reading values on failed refresh.

Optional `planStart`, `planEnd` and `daysLeft` extend the existing CLI contract. Subscription timestamps come only from console-reported millisecond epochs; `daysLeft = ceil((planEnd - now) / 24h)`. Missing/invalid periods stay omitted and display **未提供**. Window `resetsInDays` follows the same ceiling rule; expired periods can be zero or negative. Neither countdown implies unused quota.

For a service that cannot reach the interactive browser, refresh a protected cache periodically from the user session:

```powershell
.\bin\bailian-quota.exe --json --timeout 120s --cache-file C:\ProgramData\cpa-qwen-quota\qwen-quota.json
```

Then set `command-args: ["--json", "--cache-only", "--cache-file", "C:/ProgramData/cpa-qwen-quota/qwen-quota.json", "--max-age", "45m"]`. Cache-only never starts a browser and fails honestly for missing, invalid or stale readings. Refresh buttons rerun the configured CLI; in cache-only mode they reload the last observation, **not** the console. The page always shows the CLI's `observedAt`. Old caches without a subscription period remain readable but cannot reveal remaining plan days until a successful console refresh by the rebuilt CLI.

## Testing

```powershell
$env:CGO_ENABLED = '1'
go test ./... -cover
go vet ./...
```

Tests use dummy keys and mocked HTTP/CLI processes. Live acceptance must use an isolated core on **8399**, a scratch directory outside every repository, its own auth/config, and the built DLL. Do not change/restart the live **8317** instance. Remove scratch credentials and stop the isolated process afterward.

## Known limits

- CLIProxyAPI core v8.0.15's management `/plugins` response does not expose `auth_provider` or `executor` capability booleans, and its `quota_provider` field is the provider identifier string. The native registration envelope declares the booleans; credential/model/execution/quota endpoints demonstrate behavior. This is a host response-schema limitation, not a reason to invent endpoint fields.
- No OAuth flow or refresh token: these are manually configured plan keys. Refresh preserves the credential rather than inventing a token.
- Exhausted quota produces the upstream error, not a successful completion. The acceptance account's monthly quota is 100% used, resetting `2026-10-18T00:00:00+08:00`; a live **429** is expected until reset.
- Quota requires a separate console login, depends on private console RPC response schemas and browser availability, and is not automatically associated with each API key's account.
- Browser mode needs an already logged-in connected browser and temporarily navigates a no-focus session. Service/session-0 deployments may require cookie mode.
- Coding Plan optional enrichment is best-effort; successful public model discovery does not validate keys.
- Removed keys may leave host-managed credential files; remove/disable those via the host management flow.
- Initial fallback models may not be available under every plan/region. Client formats supported here are Chat Completions and Messages, not Responses.

## Attribution

Structure, native CGO ABI glue, provider lifecycle/credentials, host bridge, executor, protocol adapters, catalog/config/error/thinking utilities, tests and documentation organization are adapted from [massiveits/opencode-go-cliproxyapi](https://github.com/massiveits/opencode-go-cliproxyapi), MIT, Copyright (c) 2026 massiveits. See [NOTICE.md](NOTICE.md) for the file/area inventory and reproduced upstream license. New work is MIT, Copyright (c) 2026 williamxhero; see [LICENSE](LICENSE).
