# Anthropic Image Guard

[GitHub repository](https://github.com/darfink/cliproxyapi-anthropic-image-guard)

A native request-interceptor plugin for CLIProxyAPI v8.0.29 or later.
It bounds image dimensions and image history without replacing Claude authentication, routing, translation, or streaming.

## Behavior

The plugin acts only when the selected destination format is `claude`.
It processes inline images in Responses, Chat Completions, and Claude Messages requests, including image history and tool results.

- Resize images whose longest edge exceeds 2,000 pixels. Keep the aspect ratio.
- Preserve PNG transparency and JPEG orientation. Convert resized GIF and WebP images to PNG.
- Keep retained images within the dimension limit unchanged, without re-encoding.
- Leave retained remote image URLs unchanged. Do not fetch URLs.
- Evict the oldest images in batches when image history exceeds 20 MiB or 80 images. This behavior is always active.
- Leave text, tools, reasoning, image order, headers, and `cache_control` unchanged.
- Register no model, credential, scheduler, response, or streaming capabilities.
- Make no model calls or network requests. Write no image or prompt data to logs or disk.

The interceptor runs after credential selection but before protocol translation.
It reads the client payload format, not an already-translated Claude payload.

## Prompt Caching

The same image always produces the same encoded bytes within the same build and configuration.
Resizing starts with the first request, not when the image count exceeds 20.
This prevents a later count threshold from rewriting the existing image prefix.

Unchanged requests return no body replacement.
Changed requests replace only the affected image strings and, when necessary, their media types.
All other request bytes stay unchanged, including whitespace and JSON key order.
Evicted image blocks become short, stable text placeholders. Their positions and cache markers stay intact.
The plugin adds no instructions, summaries, or tools.

## Batched Image History

The image budget counts image blocks after dimension normalization, including base64 data and JSON overhead.
When history crosses 20 MiB or 80 images, the plugin evicts the oldest images until both low watermarks fit.
The low watermarks are 10 MiB and 40 images.
This leaves headroom for additional screenshots instead of evicting one image on each new turn.

The plugin replays the ordered image history to calculate each batch boundary.
Retries, concurrent conversations, image-cache eviction, and process restarts do not change that calculation.
Between batches, retained image bytes and earlier placeholders remain unchanged.
For 1 MiB images, tests show one eviction event per 11 added images after the first event.
For small images, the count limit gives one event per 41 added images.

Eviction changes the cached prefix from the earliest replaced image onward.
That cost occurs at batch boundaries, not on every turn.
The model cannot see omitted images unless you provide them again.
Original files and saved conversation history remain unchanged.

Cache stability applies to the same build and configuration. Go toolchain or architecture changes can change resized image bytes.
Compatibility tests compare against the original encoder on the same toolchain; they do not guarantee identical bytes across different builds.

The newest image always stays in the request.
Required computer-tool screenshot objects also stay intact because text cannot replace them without breaking the protocol.
Very large or protected screenshots can leave less headroom between batches.
If those required images cannot fit, the plugin returns an explicit error instead of an invalid tool result.

An existing conversation can lose a cached prefix once when the plugin repairs an image in that prefix.
This initial cache miss is unavoidable when the stored image violates the selected limit.
Changing the image policy or encoding implementation can also change cached image bytes.
The plugin cannot guarantee Anthropic cache hits, billing, or account affinity.

The in-memory image cache saves decoding work; it is separate from Anthropic's prompt cache.
Eviction or process restart does not change the encoded result.
The cache stores content hashes and normalized results, not credentials or complete conversations.

## Resource Limits

The cache uses both a byte budget and an entry limit.
One worker serializes image decoding and encoding, including concurrent requests for the same image.
Image headers are examined before full decoding.

Default limits:

| Limit | Default |
| --- | --- |
| Maximum image edge | 2,000 pixels |
| Image cache budget | 64 MiB |
| Image cache entries | 256 |
| Full decode budget | 32 million pixels per image |
| Base64 processing limit | 16 MiB per image |
| Resized file limit | 7 million bytes before base64 encoding |
| Image history high watermarks | 20 MiB or 80 images |
| Image history low watermarks | 10 MiB and 40 images |

The image guard does not reject a conversation because its JSON body exceeds 64 MiB.
It streams base64 data for header checks and validates the complete data before it accepts an image.
It decodes image pixels only when resizing is necessary.
The host SDK still buffers complete requests, so request memory use grows with conversation size.
The native bridge borrows the host's RPC buffer for the duration of each call instead of copying the complete envelope.
It rejects lengths that exceed the platform's Go slice address range, not an arbitrary conversation-size threshold.
Host and provider request limits still apply.

If an inline image cannot be processed safely, the plugin returns an explicit `400` error.
It does not remove the image, substitute a summary, or return a partially changed request.

## Build And Test

Requirements: Go 1.26+, a C compiler, and CGO enabled.
Always use the `debug` build tag for local builds and tests.

```sh
go test -race -tags debug ./...
go vet ./...
go build -tags debug -buildmode=c-shared -o dist/anthropic-image-guard.dylib .
```

For Linux or Windows, change the output extension to `.so` or `.dll`.

The offline host test loads the native library into a separate, unmodified CLIProxyAPI process.
It uses dummy credentials and a loopback-only mock Claude endpoint.
It verifies Messages and Responses requests with streaming and non-streaming clients.

```sh
node tools/smoke-host.mjs
node tools/smoke-host.mjs --large
node tools/smoke-host.mjs --huge
```

The `--large` test sends image histories larger than 64 MiB through the native bridge.
The mock provider enforces a 32 MB request limit. The test verifies batch eviction without changes to retained image bytes.
Both tests verify unchanged text and rejection of invalid images before model execution.
The optional `--huge` test exercises a 200 MiB image history and an RPC envelope larger than 256 MiB.
It uses more memory than the standard tests and runs one streaming Responses request against the mock provider.

Set `CLIPROXYAPI_BINARY` to select another host executable.
Pass the library path as the script's first argument to select another plugin build.
The script requires Node.js 18 or later and makes no paid API calls.

An optional private replay test accepts a captured Responses JSON request.
It never forwards or prints that request.

```sh
IMAGE_GUARD_REPLAY_REQUEST=/private/path/request.json \
  go test -tags debug ./internal/guard -run '^TestCapturedRequestReplay$' -v

IMAGE_GUARD_REPLAY_ROLLOUT=/private/path/history.jsonl \
  go test -tags debug ./internal/guard -run '^TestCapturedRolloutReplay$' -v
```

The private history test reads Responses items after the last compaction boundary.
It reports only image counts, byte totals, and batch positions. It never writes or forwards the conversation.

## Installation

Copy the library into the host's platform directory, for example:

```text
~/.cli-proxy-api/plugins/darwin/arm64/anthropic-image-guard.dylib
```

Merge this entry into the existing plugin configuration. Keep other plugin entries.

```yaml
plugins:
  enabled: true
  configs:
    anthropic-image-guard:
      enabled: true
      max-edge: 2000
      cache-mib: 64
      cache-entries: 256
      max-decode-pixels: 32000000
```

For a hot reload, install the new library with a versioned filename, such as `anthropic-image-guard-v0.2.0.dylib`.
Then save a configuration change for the plugin, such as an explicit `priority: 0`, to trigger the host configuration reload.
The host selects the newer library without a service restart.
If hot reload is unavailable, restart the service when active requests finish.
Do not patch the Homebrew executable or change the Codex provider URL.

This repository contains the source and build instructions. Tagged release archives are not available yet.

## Limits

This plugin addresses inline image dimensions and image history, not every provider request limit.
The fixed image budget reserves about 11 MB for other content. Large text, tools, or PDFs can still exceed the provider limit.
PDF eviction is not supported.
It cannot repair encrypted image data or remote URLs.
It does not change images serialized as plain text inside tool results.

Resizing can reduce small visual detail and change screenshot pixel coordinates.
Computer-use tools must retain their own coordinate mapping.
Original files and Codex's saved history remain unchanged.

Native plugins run inside the host process. Load only trusted builds.

## References

- [Anthropic image limits](https://platform.claude.com/docs/en/build-with-claude/vision#request-limits)
- [Claude Code batched image eviction](https://code.claude.com/docs/en/prompt-caching#accumulating-many-images)
- [CLIProxyAPI request interceptors](https://cliproxyapi.gh.miniasp.com/plugin/request-interceptor.html)
- [CLIProxyAPI issue 5539](https://github.com/router-for-me/CLIProxyAPI/issues/5539)
