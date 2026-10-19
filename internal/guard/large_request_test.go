package guard

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func TestLargeImageHistory(t *testing.T) {
	for _, protocol := range []string{"responses", "messages"} {
		for _, width := range []int{8, 384} {
			t.Run(fmt.Sprintf("%s/width=%d", protocol, width), func(t *testing.T) {
				g := fixtureGuard(t)
				encoded := fixtureImage(t, "png", width, 24, 1)
				container, imagePath := "input", "image_url"
				block := `{"type":"input_image","image_url":"` + dataURL("png", encoded) + `"}`
				if protocol == "messages" {
					container, imagePath = "messages", "source.data"
					block = `{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` + encoded + `"}}`
				}
				// Large text before and inside the selected array also exercises
				// absolute edit offsets. The old guard rejected this entire request.
				body := []byte(` { "metadata":{"opaque":"prefix"}, "` + container + `":[{"role":"user","content":[{"type":"text","text":"`)
				body = append(body, bytes.Repeat([]byte("x"), 65<<20)...)
				body = append(body, []byte(`"},`+block+`],"cache_control":{"type":"ephemeral"}}],"tools":[{"name":"unchanged"}],"stream":true } `)...)
				before := sha256.Sum256(body)
				out, err := g.Rewrite(body)
				if err != nil {
					t.Fatal(err)
				}
				if sha256.Sum256(body) != before {
					t.Fatal("input body was mutated")
				}
				if width <= g.cfg.MaxEdge {
					if out != nil {
						t.Fatal("large request with a small image must return no replacement")
					}
					return
				}
				path := container + ".0.content.1." + imagePath
				oldValue, newValue := gjson.GetBytes(body, path), gjson.GetBytes(out, path)
				if oldValue.Raw == newValue.Raw || !gjson.ValidBytes(out) {
					t.Fatal("large image was not rewritten")
				}
				url := newValue.Str
				if protocol == "messages" {
					url = dataURL("png", url)
				}
				img, _ := decodedImage(t, url)
				if img.Bounds().Dx() != 256 || img.Bounds().Dy() != 16 {
					t.Fatalf("unexpected image dimensions: %v", img.Bounds())
				}
				if !bytes.Equal(bytes.Replace(out, []byte(newValue.Raw), []byte(oldValue.Raw), 1), body) {
					t.Fatal("bytes outside the image changed")
				}
				replay, err := g.Rewrite(body)
				if err != nil || !bytes.Equal(replay, out) {
					t.Fatalf("warm replay changed bytes: %v", err)
				}
			})
		}
	}
}

func TestInvalidBase64AfterImageHeader(t *testing.T) {
	for _, width := range []int{8, 384} {
		raw, err := base64.StdEncoding.DecodeString(fixtureImage(t, "png", width, 24, 1))
		if err != nil {
			t.Fatal(err)
		}
		// Ensure padding, so another base64 block is invalid rather than valid
		// encoded trailing bytes after the image file.
		raw = append(raw, make([]byte, 3-len(raw)%3)...)
		raw = append(raw, 0)
		encoded := base64.StdEncoding.EncodeToString(raw)
		for _, suffix := range []string{"%%%%", "AAAA", strings.Repeat("\n", 4096) + "%"} {
			if _, err := base64.StdEncoding.DecodeString(encoded + suffix); err == nil {
				t.Fatal("test fixture must contain invalid base64")
			}
			g := fixtureGuard(t)
			if _, err := g.normalize(encoded + suffix); err == nil {
				t.Fatalf("accepted invalid base64 after a valid image, width=%d", width)
			}
			if len(g.entries) != 0 {
				t.Fatal("invalid image result was cached")
			}
		}
	}
}

func TestPerImageEncodedBudget(t *testing.T) {
	_, err := fixtureGuard(t).normalize(strings.Repeat("A", (16<<20)+4))
	if err == nil || !strings.Contains(err.Error(), "16 MiB") {
		t.Fatalf("per-image limit was not retained: %v", err)
	}
}

func TestBase64PaddingAcrossDecoderChunks(t *testing.T) {
	raw, err := base64.StdEncoding.DecodeString(fixtureImage(t, "png", 8, 24, 1))
	if err != nil {
		t.Fatal(err)
	}
	// A padded image ending exactly at the stream decoder's input boundary
	// must not make a later base64 block look like valid independent input.
	for _, length := range []int{767, 1535, 3071} {
		padded := append(bytes.Clone(raw), make([]byte, length-len(raw))...)
		encoded := base64.StdEncoding.EncodeToString(padded)
		if _, err := fixtureGuard(t).normalize(encoded + "\r\n"); err != nil {
			t.Fatalf("valid base64 with trailing newlines rejected: %v", err)
		}
		if _, err := fixtureGuard(t).normalize(encoded + "AAAA"); err == nil {
			t.Fatalf("accepted data after padding at byte %d", length)
		}
	}
}
