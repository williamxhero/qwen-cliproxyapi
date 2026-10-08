# Third-party notices and attribution

## OpenCode Go CLIProxyAPI plugin

This project copies and adapts portions of the MIT-licensed
[opencode-go-cliproxyapi](https://github.com/massiveits/opencode-go-cliproxyapi),
Copyright (c) 2026 massiveits. The read-only local reference was
`D:\WILL\AGENT\CPA\opencode-go-cliproxyapi`, revision
`7b36b2323e89b37250151c3acef96d2fc323b5ed`.

Copied/adapted files and areas (paths refer to this project):

- `main.go`: native C ABI definitions, CGO registration/dispatch/free/shutdown exports, host callback bridge and buffer guards.
- `internal/plugin/plugin.go`, `auth.go`, `executor.go`, `hostbridge.go`, `debug_release.go`: lifecycle, capability/metadata registration, credential materialisation/parse/labels, executor/error/stream handling and host HTTP/auth/log/stream bridge.
- `internal/plugin/auth_test.go`, `hostbridge_test.go`, `hostbridge_stream_test.go`: adapted reference regression tests. `test_support_test.go` derives its test doubles from the reference's `plugin_test.go`.
- `internal/plugin/management.go`: quota-info and quota-usage routes plus the embedded quota resource, adapted from the reference's management registration/dispatch/resource envelope. `quota.go` is a new CLI runner/contract mapper using the reference's selected-credential and quota-mapping idioms. `redact.go`, `provider_test.go`, `executor_limits_test.go`, `quota_test.go` and `management_test.go` are new work.
- `resources/embed.go`, `resources/quota_page.html`: Go embedding, same-origin management authentication (`cli-proxy-auth` storage decoding), parent-theme synchronization, DOM-safe card rendering and refresh patterns adapted from the reference's resource page; rewritten for Qwen plan periods, quota windows, CLI observation times and honest errors.
- `internal/config/config.go`: YAML parsing, validation, environment expansion and defaults/precedence idioms, adapted for Qwen and command quota options. `config_test.go` is new.
- `internal/catalog/catalog.go`: discovery/normalisation, snapshots, prefixing and route records, adapted to Qwen's chat-only upstream and documented fallback list. `catalog_test.go` is new.
- `internal/errclass/errclass.go`, `errclass_test.go`; `internal/thinking/thinking.go`, `thinking_test.go`: error classification/envelopes and reasoning/thinking utilities with tests.
- `internal/adapter/chatcompletions/request.go`, `request_test.go`, `convert.go`, `convert_test.go`, `stream.go`, `stream_test.go`: request/response/tool/image translation and streaming mapping, including the reference's existing Anthropic-to-chat, chat-to-Anthropic and Claude SSE synthesis.
- `internal/adapter/shared/shared.go`, `shared_test.go`, `shared_bench_test.go`, `responses_tools.go`, `responses_tools_test.go`: shared decoding, content/tool handling and SSE utilities with tests.
- Reference `internal/adapter/messages/` and `responses/` files were studied but not copied: this provider has no Messages or Responses upstream. Messages clients use the reference's chat-completions adapter and shared Claude utilities; no unused upstream adapter is bundled.
- `README.md`: section organisation and configuration/build/testing/credential/quota explanations adapted from the reference, rewritten for this project's Qwen provider and CLI.
- `go.mod` / `go.sum`: module/SDK dependency setup follows the reference (CLIProxyAPI v8.0.0 and yaml.v3); module identity changed to this project.

The `cmd/bailian-quota/` and `internal/bailianquota/` CLI implementation is
new work in this repository. No existing `aliyun-bss-quota-cliproxyapi/cmd/bailian-quota`
was present when work began. Read-only RPC fixture/schema context from that project
was used to verify console request encoding and plan/status fields; no cookies,
tokens or credentials were copied. Neither reference repository was modified.

### Upstream MIT license (reproduced in full)

MIT License

Copyright (c) 2026 massiveits

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.

## New work

New work is MIT licensed, Copyright (c) 2026 williamxhero. See `LICENSE`.
Dependency licenses remain with their respective owners.
