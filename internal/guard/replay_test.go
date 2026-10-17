package guard

import (
	"bytes"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/tidwall/gjson"
)

// This optional offline regression uses a local captured request. The request
// is never committed, printed, logged, or forwarded to any model provider.
func TestCapturedRequestReplay(t *testing.T) {
	path := os.Getenv("IMAGE_GUARD_REPLAY_REQUEST")
	if path == "" {
		t.Skip("set IMAGE_GUARD_REPLAY_REQUEST for a private offline replay")
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	g, err := New(DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	out, err := g.Rewrite(body)
	if err != nil {
		t.Fatal(err)
	}
	cold := time.Since(start)
	start = time.Now()
	warm, err := g.Rewrite(body)
	if err != nil {
		t.Fatal(err)
	}
	warmDuration := time.Since(start)
	if !bytes.Equal(warm, out) {
		t.Fatal("warm replay changes request bytes")
	}
	var collect func(gjson.Result) []gjson.Result
	collect = func(v gjson.Result) (images []gjson.Result) {
		if v.Get("type").Str == "input_image" && strings.HasPrefix(v.Get("image_url").Str, "data:image/") {
			return []gjson.Result{v.Get("image_url")}
		}
		if v.IsArray() || v.IsObject() {
			v.ForEach(func(_, child gjson.Result) bool { images = append(images, collect(child)...); return true })
		}
		return
	}
	before := collect(gjson.ParseBytes(body))
	after := collect(gjson.ParseBytes(out))
	if len(before) <= 20 || len(before) != len(after) {
		t.Fatal("expected an unchanged many-image request")
	}
	restored := append([]byte{}, out...)
	changed := 0
	expectedChanges := 0
	for i, image := range after {
		original, _ := decodedImage(t, before[i].Str)
		if original.Bounds().Dx() > 2000 || original.Bounds().Dy() > 2000 {
			expectedChanges++
		}
		decoded, _ := decodedImage(t, image.Str)
		if decoded.Bounds().Dx() > 2000 || decoded.Bounds().Dy() > 2000 {
			t.Fatal("oversized image remains")
		}
		if image.Raw != before[i].Raw {
			changed++
			restored = bytes.Replace(restored, []byte(image.Raw), []byte(before[i].Raw), 1)
		}
	}
	if changed == 0 || changed != expectedChanges || !bytes.Equal(restored, body) {
		t.Fatalf("changed=%d expected=%d unrelated-bytes-preserved=%t", changed, expectedChanges, bytes.Equal(restored, body))
	}
	if again, err := g.Rewrite(out); err != nil || again != nil {
		t.Fatalf("already repaired history changed: %v", err)
	}
	t.Logf("images=%d resized=%d cold=%s warm=%s; all other bytes unchanged", len(before), changed, cold, warmDuration)
}
