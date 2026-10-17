# Release Notes

## 0.1.0 - Unreleased

- Add a standalone Claude-only request interceptor for CLIProxyAPI v8.
- Normalize inline images before translation, including tool results and earlier image turns.
- Preserve unaffected request bytes, image transparency, and JPEG orientation.
- Apply a fixed edge policy from the first request to avoid image-count-dependent prefix changes.
- Cache deterministic image results with byte and entry limits.
- Bound image decoding and return explicit errors instead of silently removing images.
- Add offline unit, concurrency, replay, and native host tests.
