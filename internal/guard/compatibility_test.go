package guard

import (
	"crypto/sha256"
	"fmt"
	"testing"
)

// Pin v0.1.0 image bytes so parser and memory improvements cannot invalidate
// existing image prefixes in the provider's prompt cache.
func TestNormalizationCacheCompatibility(t *testing.T) {
	for _, tc := range []struct {
		name, format, encoded, expected string
		width, height, maxEdge          int
	}{
		{name: "png", format: "png", width: 384, height: 24, maxEdge: 256, expected: "1354fe4caff195bb6bf75c66e95cebef7896f75ca303a7c319f663d0a4672f40"},
		{name: "jpeg", format: "jpeg", width: 384, height: 24, maxEdge: 256, expected: "23e96a7d52d40bc128ae7e31610b7dec0acf126d821a1f4c0fa13ade710aca1e"},
		{name: "gif", format: "gif", width: 384, height: 24, maxEdge: 256, expected: "6612b0a8a2c6d4d8454510f8f32f423a2f45886aabf201ce45a956e5ac505efd"},
		{name: "webp", encoded: "UklGRiQAAABXRUJQVlA4TBcAAAAvf8EFEAdQwaJUuoBhABLC//1SRP9TPwA=", maxEdge: 256, expected: "74c8f7873ba877f7c3fd2d96d400cb33d63a9c1af19605a0d4e828efe5a07718"},
		{name: "default-cap", format: "png", width: 3520, height: 340, maxEdge: 2000, expected: "b6baaf3c70ccd27e59a25680ffcea6e0dc0aa1bf1fafb284e9941093889b3f7e"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := DefaultConfig()
			cfg.MaxEdge = tc.maxEdge
			g, err := New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			encoded := tc.encoded
			if encoded == "" {
				encoded = fixtureImage(t, tc.format, tc.width, tc.height, 1)
			}
			result, err := g.normalize(encoded)
			if err != nil {
				t.Fatal(err)
			}
			got := fmt.Sprintf("%x", sha256.Sum256([]byte(result.data)))
			if !result.changed || got != tc.expected {
				t.Fatalf("normalized image bytes changed: %s", got)
			}
		})
	}
}
