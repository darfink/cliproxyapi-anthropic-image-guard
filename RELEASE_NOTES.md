# Release Notes

## 0.2.0 - Unreleased

- Compare image compatibility against the original encoder on the same toolchain. Test Go 1.26.0 and 1.27.2 in CI.
- Enable batched image-history eviction by default. No opt-in configuration is necessary.
- At 20 MiB or 80 images, replace the oldest image blocks with stable text placeholders down to 10 MiB and 40 images.
- Preserve retained image quality, message positions, tool results, text, and cache markers.
- Calculate batch boundaries from ordered history without mutable session state or saved conversation data.
- Keep cutoffs stable across retries, concurrent requests, process restarts, and image-cache eviction.
- Remove the separate 256 MiB native-envelope cap and avoid copying the complete RPC buffer before JSON decoding.
- Preserve the newest image and required computer-tool screenshots. Return explicit errors if required images cannot fit.
- Add cadence, protocol, prefix, concurrency, real-history replay, and native host tests with a 32 MB mock-provider limit.

## 0.1.2 - Unreleased

- Remove the 64 MiB conversation limit. Keep per-image, decode, and cache budgets.
- Stream base64 data during image checks instead of allocating a complete decoded-file buffer.
- Reject data after base64 padding, including padding at a decoder chunk boundary.
- Preserve the existing resize policy and encoded image bytes, with compatibility tests for each supported format.
- Read only protocol content containers instead of copying the complete JSON body for traversal.
- Add large-history regression tests and an offline native host test for requests larger than 64 MiB.
- Preserve text, tools, cache controls, and other request bytes in large conversations.

## 0.1.0 - Unreleased

- Add a standalone Claude-only request interceptor for CLIProxyAPI v8.
- Normalize inline images before translation, including tool results and earlier image turns.
- Preserve unaffected request bytes, image transparency, and JPEG orientation.
- Apply a fixed edge policy from the first request to avoid image-count-dependent prefix changes.
- Cache deterministic image results with byte and entry limits.
- Bound image decoding and return explicit errors instead of silently removing images.
- Add offline unit, concurrency, replay, and native host tests.
