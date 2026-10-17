package guard

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/tidwall/gjson"
)

func TestNoOpIsByteExact(t *testing.T) {
	g := fixtureGuard(t)
	small := dataURL("png", fixtureImage(t, "png", 256, 8, 0))
	for _, body := range []string{
		` { "model" : "alias", "input" : "text <>&", "cache_control" : {"type":"ephemeral"} } `,
		`{"input":[{"role":"user","content":[{"type":"input_image","image_url":"` + small + `"}]}]}`,
		`{"input":[{"role":"user","content":[{"type":"input_image","image_url":"https://example.invalid/image.png"}]}]}`,
	} {
		before := []byte(body)
		out, err := g.Rewrite(before)
		if err != nil {
			t.Fatal(err)
		}
		if out != nil {
			t.Fatal("no-op must return nil, not a reserialized body")
		}
		if !bytes.Equal(before, []byte(body)) {
			t.Fatal("input mutated")
		}
	}
}

func TestProtocolContainers(t *testing.T) {
	encoded := fixtureImage(t, "png", 384, 24, 1)
	url := dataURL("png", encoded)
	cases := []struct{ name, body, imagePath, dataPath string }{
		{"responses-message", `{"input":[{"role":"user","content":[{"type":"input_image","image_url":"` + url + `","detail":"original"}]}]}`, "input.0.content.0", "image_url"},
		{"responses-tool", `{"input":[{"type":"function_call_output","call_id":"call_a","output":[{"type":"input_image","image_url":"` + url + `"}]}]}`, "input.0.output.0", "image_url"},
		{"responses-custom-tool", `{"input":[{"type":"custom_tool_call_output","call_id":"call_a","output":[{"type":"input_image","image_url":"` + url + `"}]}]}`, "input.0.output.0", "image_url"},
		{"responses-computer-tool", `{"input":[{"type":"computer_call_output","call_id":"call_a","output":{"type":"computer_screenshot","image_url":"` + url + `"}}]}`, "input.0.output", "image_url"},
		{"escaped-image-string", `{"input":[{"role":"user","content":[{"type":"input_image","image_url":"` + strings.ReplaceAll(url, "/", `\/`) + `"}]}]}`, "input.0.content.0", "image_url"},
		{"chat", `{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"` + url + `","detail":"high"}}]}]}`, "messages.0.content.0", "image_url.url"},
		{"claude", `{"messages":[{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` + encoded + `"},"cache_control":{"type":"ephemeral"}}]}]}`, "messages.0.content.0", "source.data"},
		{"claude-tool", `{"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"tool_a","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` + encoded + `"}}]}]}]}`, "messages.0.content.0.content.0", "source.data"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g := fixtureGuard(t)
			body := []byte(tc.body)
			out, err := g.Rewrite(body)
			if err != nil {
				t.Fatal(err)
			}
			if out == nil || !gjson.ValidBytes(out) {
				t.Fatal("expected a valid rewritten request")
			}
			newData := gjson.GetBytes(out, tc.imagePath+"."+tc.dataPath).Str
			newURL := newData
			if tc.dataPath == "source.data" {
				newURL = dataURL("png", newData)
			}
			img, _ := decodedImage(t, newURL)
			if img.Bounds().Dx() != 256 || img.Bounds().Dy() != 16 {
				t.Fatalf("unexpected dimensions: %v", img.Bounds())
			}
			oldValue := gjson.GetBytes(body, tc.imagePath+"."+tc.dataPath).Raw
			newValue := gjson.GetBytes(out, tc.imagePath+"."+tc.dataPath).Raw
			if !bytes.Equal(bytes.Replace(out, []byte(newValue), []byte(oldValue), 1), body) {
				t.Fatal("bytes outside the image changed")
			}
			again, err := g.Rewrite(out)
			if err != nil || again != nil {
				t.Fatalf("rewrite is not idempotent: %v", err)
			}
		})
	}
}

func TestOnlyKnownImageContainersChange(t *testing.T) {
	g := fixtureGuard(t)
	url := dataURL("png", fixtureImage(t, "png", 384, 24, 1))
	image := `{"type":"input_image","image_url":"` + url + `"}`
	body := []byte(`{"input":[{"type":"function_call","arguments":{"content":[` + image + `]}},{"type":"reasoning","encrypted_content":"opaque"}],"tools":[{"parameters":{"content":[` + image + `]}}],"metadata":{"content":[` + image + `]}}`)
	out, err := g.Rewrite(body)
	if err != nil || out != nil {
		t.Fatalf("arbitrary JSON must not be traversed: %v", err)
	}
}

func TestCachePrefixStableAcrossTwentyImagesAndEviction(t *testing.T) {
	g := fixtureGuard(t)
	large := dataURL("png", fixtureImage(t, "png", 384, 24, 1))
	first := ` { "type" : "message", "role" : "user", "content" : [{"type":"input_image","image_url":"` + large + `"}], "cache_control":{"type":"ephemeral"} } `
	one := []byte(`{"input":[` + first + `],"stream":true}`)
	outOne, err := g.Rewrite(one)
	if err != nil {
		t.Fatal(err)
	}
	prefix := gjson.GetBytes(outOne, "input.0").Raw
	images := []string{first}
	for i := range 20 {
		small := dataURL("png", fixtureImage(t, "png", 8, 8, i))
		images = append(images, `{"role":"user","content":[{"type":"input_image","image_url":"`+small+`"}]}`)
	}
	for _, count := range []int{20, 21} {
		body := []byte(`{"input":[` + strings.Join(images[:count], ",") + `],"stream":true}`)
		out, err := g.Rewrite(body)
		if err != nil {
			t.Fatal(err)
		}
		if gjson.GetBytes(out, "input.0").Raw != prefix {
			t.Fatal("20-image boundary changed an already-normalized prefix")
		}
		if !bytes.Equal(bytes.Replace(out, []byte(prefix), []byte(gjson.GetBytes(body, "input.0").Raw), 1), body) {
			t.Fatal("later history changed")
		}
	}
	// A new process or an eviction must produce the same image bytes.
	cold := fixtureGuard(t)
	outCold, err := cold.Rewrite(one)
	if err != nil || !bytes.Equal(outOne, outCold) {
		t.Fatalf("cold cache changed bytes: %v", err)
	}
	cfg := g.cfg
	cfg.CacheEntries = 1
	evicted, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := evicted.Rewrite(one); err != nil {
		t.Fatal(err)
	}
	if _, err := evicted.normalize(fixtureImage(t, "png", 4, 4, 25)); err != nil {
		t.Fatal(err)
	}
	outEvicted, err := evicted.Rewrite(one)
	if err != nil || !bytes.Equal(outOne, outEvicted) {
		t.Fatalf("eviction changed bytes: %v", err)
	}
}

func TestFormatsAndAlpha(t *testing.T) {
	for _, format := range []string{"png", "jpeg", "gif"} {
		t.Run(format, func(t *testing.T) {
			g := fixtureGuard(t)
			url := dataURL(format, fixtureImage(t, format, 384, 24, 2))
			body := []byte(`{"input":[{"role":"user","content":[{"type":"input_image","image_url":"` + url + `"}]}]}`)
			out, err := g.Rewrite(body)
			if err != nil {
				t.Fatal(err)
			}
			img, gotFormat := decodedImage(t, gjson.GetBytes(out, "input.0.content.0.image_url").Str)
			want := "png"
			if format == "jpeg" {
				want = "jpeg"
			}
			if gotFormat != want {
				t.Fatalf("got %s, want %s", gotFormat, want)
			}
			if format == "png" {
				_, _, _, alpha := img.At(0, 0).RGBA()
				if alpha == 65535 {
					t.Fatal("PNG transparency was lost")
				}
			}
		})
	}
}

func TestWebPAndMediaType(t *testing.T) {
	g := fixtureGuard(t)
	// Generated lossless RGBA WebP, 384x24. No external encoder is required.
	encoded := "UklGRiQAAABXRUJQVlA4TBcAAAAvf8EFEAdQwaJUuoBhABLC//1SRP9TPwA="
	body := []byte(`{"messages":[{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/webp","data":"` + encoded + `"}}]}]}`)
	out, err := g.Rewrite(body)
	if err != nil {
		t.Fatal(err)
	}
	if gjson.GetBytes(out, "messages.0.content.0.source.media_type").Str != "image/png" {
		t.Fatal("media type did not follow the PNG conversion")
	}
	img, format := decodedImage(t, dataURL("png", gjson.GetBytes(out, "messages.0.content.0.source.data").Str))
	_, _, _, alpha := img.At(0, 0).RGBA()
	if format != "png" || img.Bounds().Dx() != 256 || alpha == 65535 {
		t.Fatal("WebP dimensions or transparency changed incorrectly")
	}
}

func TestJPEGOrientation(t *testing.T) {
	g := fixtureGuard(t)
	raw, err := base64.StdEncoding.DecodeString(fixtureImage(t, "jpeg", 384, 24, 0))
	if err != nil {
		t.Fatal(err)
	}
	// Little-endian EXIF IFD0, orientation 6 (90 degrees clockwise).
	exif := []byte{'E', 'x', 'i', 'f', 0, 0, 'I', 'I', 42, 0, 8, 0, 0, 0, 1, 0, 0x12, 1, 3, 0, 1, 0, 0, 0, 6, 0, 0, 0, 0, 0, 0, 0}
	segment := append([]byte{0xff, 0xe1, 0, byte(len(exif) + 2)}, exif...)
	raw = append(append(append([]byte{}, raw[:2]...), segment...), raw[2:]...)
	url := dataURL("jpeg", base64.StdEncoding.EncodeToString(raw))
	body := []byte(`{"input":[{"role":"user","content":[{"type":"input_image","image_url":"` + url + `"}]}]}`)
	out, err := g.Rewrite(body)
	if err != nil {
		t.Fatal(err)
	}
	img, _ := decodedImage(t, gjson.GetBytes(out, "input.0.content.0.image_url").Str)
	if img.Bounds().Dx() != 16 || img.Bounds().Dy() != 256 {
		t.Fatalf("EXIF rotation lost: %v", img.Bounds())
	}
}

func TestInvalidAndOversizedInputs(t *testing.T) {
	for _, encoded := range []string{"%%%", base64.StdEncoding.EncodeToString([]byte("not an image")), fixtureHugeHeader(t)} {
		body := fixtureRequest(t, map[string]any{"messages": []any{map[string]any{"role": "user", "content": []any{map[string]any{"type": "image", "source": map[string]any{"type": "base64", "media_type": "image/png", "data": encoded}}}}}})
		out, err := fixtureGuard(t).Rewrite(body)
		if err == nil || out != nil {
			t.Fatal("invalid image must not pass or become a partial rewrite")
		}
		if strings.Contains(err.Error(), encoded) {
			t.Fatal("error contains image data")
		}
	}
	if _, err := fixtureGuard(t).Rewrite([]byte(`{"input":`)); err == nil {
		t.Fatal("invalid JSON accepted")
	}
}

func TestConcurrentRequests(t *testing.T) {
	g := fixtureGuard(t)
	url := dataURL("png", fixtureImage(t, "png", 384, 24, 1))
	body := []byte(`{"input":[{"role":"user","content":[{"type":"input_image","image_url":"` + url + `"}]}]}`)
	var wg sync.WaitGroup
	results := make(chan string, 20)
	for range 20 {
		wg.Go(func() {
			out, err := g.Rewrite(body)
			if err != nil {
				results <- "error: " + err.Error()
				return
			}
			results <- string(out)
		})
	}
	wg.Wait()
	close(results)
	first := ""
	for value := range results {
		if strings.HasPrefix(value, "error:") {
			t.Fatal(value)
		}
		if first == "" {
			first = value
		} else if value != first {
			t.Fatal("concurrent rewrites differ")
		}
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if len(g.entries) != 1 {
		t.Fatal("duplicate requests did not share a cached result")
	}
}

func TestCacheBoundsAndDisable(t *testing.T) {
	for _, disabled := range []bool{false, true} {
		cfg := DefaultConfig()
		cfg.MaxEdge, cfg.CacheEntries, cfg.CacheMiB = 256, 2, 1
		if disabled {
			cfg.CacheMiB = 0
		}
		g, err := New(cfg)
		if err != nil {
			t.Fatal(err)
		}
		for i := range 6 {
			if _, err := g.normalize(fixtureImage(t, "png", 384, 24, i)); err != nil {
				t.Fatal(err)
			}
		}
		if len(g.entries) > 2 || g.cacheBytes > 1<<20 {
			t.Fatal("cache bounds exceeded")
		}
		if disabled && len(g.entries) != 0 {
			t.Fatal("disabled cache stores entries")
		}
	}
}

func TestCacheByteBudget(t *testing.T) {
	cfg := DefaultConfig()
	cfg.CacheMiB = 1
	g, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	a, b, c := sha256.Sum256([]byte("a")), sha256.Sum256([]byte("b")), sha256.Sum256([]byte("c"))
	g.remember(a, normalized{data: strings.Repeat("a", 700_000), changed: true})
	g.remember(b, normalized{data: strings.Repeat("b", 700_000), changed: true})
	if _, ok := g.cached(a); ok {
		t.Fatal("byte budget did not evict the oldest entry")
	}
	if _, ok := g.cached(b); !ok {
		t.Fatal("latest entry missing")
	}
	g.remember(c, normalized{data: strings.Repeat("c", 2<<20), changed: true})
	if _, ok := g.cached(c); ok {
		t.Fatal("single oversized result was retained")
	}
	if g.cacheBytes > 1<<20 || len(g.entries) != 1 {
		t.Fatal("cache budget exceeded")
	}
}

func TestLargeTextOnlyRequestIsNotLimited(t *testing.T) {
	body := append([]byte(`{"input":"`), bytes.Repeat([]byte("x"), maxRequestBytes)...)
	body = append(body, []byte(`"}`)...)
	out, err := fixtureGuard(t).Rewrite(body)
	if err != nil || out != nil {
		t.Fatalf("text-only request changed: %v", err)
	}
}

func TestConfigBounds(t *testing.T) {
	for _, change := range []func(*Config){
		func(c *Config) { c.MaxEdge = 2048 }, func(c *Config) { c.MaxEdge = 0 },
		func(c *Config) { c.CacheMiB = -1 }, func(c *Config) { c.CacheEntries = -1 },
		func(c *Config) { c.MaxDecodePixels = 100_000_000 },
	} {
		cfg := DefaultConfig()
		change(&cfg)
		if _, err := New(cfg); err == nil {
			t.Fatal("invalid configuration accepted")
		}
	}
}

func BenchmarkWarmReplay(b *testing.B) {
	g := fixtureGuard(b)
	url := dataURL("png", fixtureImage(b, "png", 3520, 340, 1))
	body := []byte(fmt.Sprintf(`{"input":[{"type":"function_call_output","output":[{"type":"input_image","image_url":%q}]}]}`, url))
	if _, err := g.Rewrite(body); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.SetBytes(int64(len(body)))
	b.ResetTimer()
	for b.Loop() {
		if _, err := g.Rewrite(body); err != nil {
			b.Fatal(err)
		}
	}
}
