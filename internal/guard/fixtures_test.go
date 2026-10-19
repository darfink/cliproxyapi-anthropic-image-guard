package guard

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"hash/crc32"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"strings"
	"testing"
)

func fixtureImage(t testing.TB, format string, width, height, seed int) string {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, width, height))
	for y := range height {
		for x := range width {
			img.SetNRGBA(x, y, color.NRGBA{R: uint8(x + seed), G: uint8(y + seed), B: 170, A: uint8(100 + x%156)})
		}
	}
	var out bytes.Buffer
	var err error
	switch format {
	case "jpeg":
		err = jpeg.Encode(&out, img, &jpeg.Options{Quality: 95})
	case "gif":
		err = gif.Encode(&out, img, nil)
	default:
		err = png.Encode(&out, img)
	}
	if err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(out.Bytes())
}

func fixturePaddedImage(t testing.TB, encodedBytes int) string {
	t.Helper()
	raw, err := base64.StdEncoding.DecodeString(fixtureImage(t, "png", 8, 8, 1))
	if err != nil {
		t.Fatal(err)
	}
	// Valid image with trailing bytes: exercise byte budgets without large pixel
	// allocations or dependencies on a random, hard-to-compress fixture.
	size := encodedBytes / 4 * 3
	if size < len(raw) {
		t.Fatal("padded fixture is too small")
	}
	return base64.StdEncoding.EncodeToString(append(raw, make([]byte, size-len(raw))...))
}

func fixtureImageHistory(block string, count int, suffix string) []byte {
	return []byte(` { "input" : [{"role":"user","content":[` + strings.TrimSuffix(strings.Repeat(block+",", count), ",") +
		`,{"type":"input_text","text":"unchanged text"}]}], "tools":[{"name":"unchanged"}], "metadata":{"suffix":"` + suffix + `"} } `)
}

func fixtureGuard(t testing.TB) *Guard {
	t.Helper()
	cfg := DefaultConfig()
	cfg.MaxEdge = 256
	g, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return g
}

func dataURL(format, encoded string) string { return "data:image/" + format + ";base64," + encoded }

func fixtureRequest(t testing.TB, value any) []byte {
	t.Helper()
	b, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func fixtureHugeHeader(t testing.TB) string {
	t.Helper()
	raw, err := base64.StdEncoding.DecodeString(fixtureImage(t, "png", 1, 1, 0))
	if err != nil {
		t.Fatal(err)
	}
	binary.BigEndian.PutUint32(raw[16:20], 8000)
	binary.BigEndian.PutUint32(raw[20:24], 8000)
	binary.BigEndian.PutUint32(raw[29:33], crc32.ChecksumIEEE(raw[12:29]))
	return base64.StdEncoding.EncodeToString(raw)
}

func decodedImage(t testing.TB, url string) (image.Image, string) {
	t.Helper()
	comma := bytes.IndexByte([]byte(url), ',')
	raw, err := base64.StdEncoding.DecodeString(url[comma+1:])
	if err != nil {
		t.Fatal(err)
	}
	img, format, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	return img, format
}
