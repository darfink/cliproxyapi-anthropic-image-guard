package guard

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/jpeg"
	"image/png"
	"testing"

	"github.com/disintegration/imaging"
)

// Compare against v0.1.0 on the same toolchain so parser and memory improvements
// cannot invalidate existing image prefixes. Encoded PNG bytes can differ across
// Go versions, and resampling can differ across architectures, so a hash captured
// on a developer's machine is not a portable compatibility baseline.
func TestNormalizationCacheCompatibility(t *testing.T) {
	for _, tc := range []struct {
		name, format, encoded  string
		width, height, maxEdge int
	}{
		{name: "png", format: "png", width: 384, height: 24, maxEdge: 256},
		{name: "jpeg", format: "jpeg", width: 384, height: 24, maxEdge: 256},
		{name: "gif", format: "gif", width: 384, height: 24, maxEdge: 256},
		{name: "webp", encoded: "UklGRiQAAABXRUJQVlA4TBcAAAAvf8EFEAdQwaJUuoBhABLC//1SRP9TPwA=", maxEdge: 256},
		{name: "default-cap", format: "png", width: 3520, height: 340, maxEdge: 2000},
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
			if !result.changed || result != legacyNormalization(t, encoded, tc.maxEdge) {
				t.Fatal("normalized image bytes differ from v0.1.0 on this toolchain")
			}
		})
	}
}

// Keep this reference independent of the production normalization path. It
// freezes v0.1.0's full-buffer decode, resize, and encoding policy for valid
// oversized fixtures; safety checks are covered by the other guard tests.
func legacyNormalization(t testing.TB, encoded string, maxEdge int) normalized {
	t.Helper()
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatal(err)
	}
	_, format, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := imaging.Decode(bytes.NewReader(raw), imaging.AutoOrientation(true))
	if err != nil {
		t.Fatal(err)
	}
	resized := imaging.Fit(decoded, maxEdge, maxEdge, imaging.Lanczos)
	var output bytes.Buffer
	if format == "jpeg" {
		err = jpeg.Encode(&output, resized, &jpeg.Options{Quality: 90})
	} else {
		format = "png"
		encoder := png.Encoder{CompressionLevel: png.DefaultCompression}
		err = encoder.Encode(&output, resized)
	}
	if err != nil {
		t.Fatal(err)
	}
	return normalized{data: base64.StdEncoding.EncodeToString(output.Bytes()), format: format, changed: true}
}
