# Anthropic Image Guard

[GitHub repository](https://github.com/darfink/cliproxyapi-anthropic-image-guard)

A native request-interceptor plugin for CLIProxyAPI v8.0.29 or later.
It prevents Anthropic's many-image dimension error without replacing Claude authentication, routing, translation, or streaming.

## Behavior

The plugin acts only when the selected destination format is `claude`.
It processes inline images in Responses, Chat Completions, and Claude Messages requests, including image history and tool results.

- Resize images whose longest edge exceeds 2,000 pixels. Keep the aspect ratio.
- Preserve PNG transparency and JPEG orientation. Convert resized GIF and WebP images to PNG.
- Leave images within the limit unchanged, without re-encoding.
- Leave remote image URLs unchanged. Do not fetch URLs.
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
The plugin adds no instructions, summaries, tools, or token-consuming notices.

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
| Request processing limit | 64 MiB |
| Base64 processing limit | 16 MiB per image |
| Resized file limit | 7 million bytes before base64 encoding |

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
```

Set `CLIPROXYAPI_BINARY` to select another host executable.
Pass the library path as the script's first argument to select another plugin build.
The script requires Node.js 18 or later and makes no paid API calls.

An optional private replay test accepts a captured Responses JSON request.
It never forwards or prints that request.

```sh
IMAGE_GUARD_REPLAY_REQUEST=/private/path/request.json \
  go test -tags debug ./internal/guard -run '^TestCapturedRequestReplay$' -v
```

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

Reload plugins through the management interface, or restart the service when active requests finish.
Do not patch the Homebrew executable or change the Codex provider URL.

This repository contains the source and build instructions. Tagged release archives are not available yet.

## Limits

This plugin addresses inline image dimensions, not every provider image or request limit.
It cannot repair encrypted image data or remote URLs.
It does not change images serialized as plain text inside tool results.

Resizing can reduce small visual detail and change screenshot pixel coordinates.
Computer-use tools must retain their own coordinate mapping.
Original files and Codex's saved history remain unchanged.

Native plugins run inside the host process. Load only trusted builds.

## References

- [Anthropic image limits](https://platform.claude.com/docs/en/build-with-claude/vision#request-limits)
- [CLIProxyAPI request interceptors](https://cliproxyapi.gh.miniasp.com/plugin/request-interceptor.html)
- [CLIProxyAPI issue 5539](https://github.com/router-for-me/CLIProxyAPI/issues/5539)
