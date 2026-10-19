package guard

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"image"
	"io"
	"os"
	"sort"
	"strings"
	"testing"

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
	verifyCapturedReplay(t, body)
}

func TestCapturedRolloutReplay(t *testing.T) {
	path := os.Getenv("IMAGE_GUARD_REPLAY_ROLLOUT")
	if path == "" {
		t.Skip("set IMAGE_GUARD_REPLAY_ROLLOUT for a private offline history replay")
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	decoder := json.NewDecoder(f)
	var items []json.RawMessage
	for {
		var record struct {
			Type    string          `json:"type"`
			Payload json.RawMessage `json:"payload"`
		}
		if err := decoder.Decode(&record); err == io.EOF {
			break
		} else if err != nil {
			t.Fatal("invalid private history JSON")
		}
		if record.Type == "compacted" {
			items = nil
		} else if record.Type == "response_item" {
			items = append(items, record.Payload)
		}
	}
	body, err := json.Marshal(map[string]any{"model": "claude-opus-5-5", "input": items})
	if err != nil {
		t.Fatal(err)
	}
	verifyCapturedReplay(t, body)
}

func verifyCapturedReplay(t *testing.T, body []byte) {
	t.Helper()
	g, err := New(DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	out, err := g.Rewrite(body)
	if err != nil {
		t.Fatal(err)
	}
	if out == nil {
		out = body
	}
	if !gjson.ValidBytes(out) {
		t.Fatal("invalid repaired JSON")
	}
	warm, err := g.Rewrite(body)
	if err != nil || (warm != nil && !bytes.Equal(out, warm)) {
		t.Fatal("warm replay changed image history")
	}
	cold, err := New(DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	restarted, err := cold.Rewrite(body)
	if err != nil || (restarted != nil && !bytes.Equal(out, restarted)) {
		t.Fatal("restart changed image history")
	}
	var originals []historyImage
	var restore []edit
	removed := 0
	var walk func(gjson.Result, gjson.Result)
	walk = func(before, after gjson.Result) {
		url := before.Get("image_url")
		if before.Get("type").Str == "input_image" && strings.HasPrefix(url.Str, "data:image/") {
			wireBytes := len(before.Raw)
			comma := strings.IndexByte(url.Str, ',')
			normal, err := g.normalize(url.Str[comma+1:])
			if err != nil {
				t.Fatal("private image normalization failed")
			}
			if normal.changed {
				encoded, _ := json.Marshal(dataURL(normal.format, normal.data))
				wireBytes += len(encoded) - len(url.Raw)
			}
			originals = append(originals, historyImage{bytes: wireBytes})
			if after.Get("type").Str == "input_text" && after.Get("text").Str == omittedImage {
				removed++
			} else {
				newURL := after.Get("image_url").Str
				if !strings.HasPrefix(newURL, "data:image/") {
					t.Fatal("image was not retained or replaced with a placeholder")
				}
				split := strings.IndexByte(newURL, ',')
				config, _, err := image.DecodeConfig(base64.NewDecoder(base64.StdEncoding, strings.NewReader(newURL[split+1:])))
				if err != nil || config.Width > 2000 || config.Height > 2000 {
					t.Fatal("retained private image has invalid dimensions")
				}
				if !normal.changed && before.Raw != after.Raw {
					t.Fatal("retained small image changed")
				}
			}
			if before.Get("cache_control").Raw != after.Get("cache_control").Raw {
				t.Fatal("private image cache marker changed")
			}
			if before.Raw != after.Raw {
				restore = append(restore, edit{after.Index, after.Index + len(after.Raw), []byte(before.Raw)})
			}
			return
		}
		if before.IsArray() {
			b, a := before.Array(), after.Array()
			if len(b) != len(a) {
				t.Fatal("private history positions changed")
			}
			for i := range b {
				walk(b[i], a[i])
			}
		} else if before.IsObject() {
			a := after.Map()
			before.ForEach(func(key, child gjson.Result) bool { walk(child, a[key.Str]); return true })
		}
	}
	walk(gjson.ParseBytes(body), gjson.ParseBytes(out))
	if len(originals) <= 20 {
		t.Fatal("expected a many-image history")
	}
	sort.Slice(restore, func(i, j int) bool { return restore[i].start < restore[j].start })
	var restored bytes.Buffer
	position := 0
	for _, e := range restore {
		restored.Write(out[position:e.start])
		restored.Write(e.value)
		position = e.end
	}
	restored.Write(out[position:])
	if !bytes.Equal(restored.Bytes(), body) {
		t.Fatal("bytes outside private image blocks changed")
	}
	if again, err := g.Rewrite(out); err != nil || again != nil {
		t.Fatal("repaired private history was not idempotent")
	}
	var events []int
	lastRemoved := 0
	for n := 1; n <= len(originals); n++ {
		evicted, err := imageEvictions(originals[:n])
		if err != nil {
			t.Fatal(err)
		}
		count := 0
		for _, value := range evicted {
			if value {
				count++
			}
		}
		if count != lastRemoved {
			if len(events) > 0 && events[len(events)-1] == n-1 {
				t.Fatal("private screenshots caused eviction on consecutive additions")
			}
			events = append(events, n)
		}
		lastRemoved = count
	}
	if removed != lastRemoved || (removed > 0 && len(out) >= 30_000_000) {
		t.Fatal("private history did not fit with provider headroom")
	}
	t.Logf("images=%d evicted=%d retained=%d bytes=%d->%d eviction-additions=%v; warm/cold/idempotent; all non-image bytes preserved",
		len(originals), removed, len(originals)-removed, len(body), len(out), events)
}
