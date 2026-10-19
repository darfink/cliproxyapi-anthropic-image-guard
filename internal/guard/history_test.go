package guard

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/tidwall/gjson"
)

func TestHistoryEvictsInBatches(t *testing.T) {
	for _, tc := range []struct {
		name        string
		size, turns int
		want        []int
	}{
		{"count", 1024, 245, []int{81, 122, 163, 204, 245}},
		{"bytes", 1 << 20, 65, []int{21, 32, 43, 54, 65}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var images []historyImage
			var events []int
			previousCutoff := 0
			for n := 1; n <= tc.turns; n++ {
				images = append(images, historyImage{bytes: tc.size})
				evicted, err := imageEvictions(images)
				if err != nil {
					t.Fatal(err)
				}
				cutoff := 0
				for cutoff < n && evicted[cutoff] {
					cutoff++
				}
				for _, removed := range evicted[cutoff:] {
					if removed {
						t.Fatal("eviction was not oldest-first")
					}
				}
				if cutoff < previousCutoff || cutoff >= n || n-cutoff > imageCountHigh || (n-cutoff)*tc.size > imageBytesHigh {
					t.Fatalf("invalid cutoff %d at turn %d", cutoff, n)
				}
				if cutoff != previousCutoff {
					events = append(events, n)
				}
				previousCutoff = cutoff
			}
			if !reflect.DeepEqual(events, tc.want) {
				t.Fatalf("eviction turns=%v, want %v", events, tc.want)
			}
		})
	}
}

func TestDefaultHistoryPolicyAndStablePrefix(t *testing.T) {
	g := fixtureGuard(t)
	block := `{"type":"input_image","image_url":"https://example.invalid/history.png"}`
	var prefix string
	for count := 81; count <= 121; count++ {
		body := fixtureImageHistory(block, count, fmt.Sprint(count))
		out, err := g.Rewrite(body)
		if err != nil {
			t.Fatal(err)
		}
		content := gjson.GetBytes(out, "input.0.content").Array()
		if len(content) != count+1 {
			t.Fatal("history positions or text were lost")
		}
		for i, value := range content[:count] {
			if i < 41 {
				if value.Get("type").Str != "input_text" || value.Get("text").Str != omittedImage {
					t.Fatalf("old image %d was not replaced", i)
				}
			} else if value.Raw != block {
				t.Fatalf("retained image %d changed", i)
			}
		}
		// The complete previously-sent conversation remains a byte-exact prefix
		// across 40 more screenshots and unrelated metadata/text changes.
		currentPrefix := string(out[:content[80].Index+len(content[80].Raw)])
		if prefix != "" && prefix != currentPrefix {
			t.Fatal("cached image prefix changed between batches")
		}
		prefix = currentPrefix
		cold, err := fixtureGuard(t).Rewrite(body)
		if err != nil || !bytes.Equal(out, cold) {
			t.Fatalf("restart changed the cutoff: %v", err)
		}
		if again, err := g.Rewrite(out); err != nil || again != nil {
			t.Fatalf("rewriting repaired history is not idempotent: %v", err)
		}
	}
}

func TestHistoryProtocolPlaceholders(t *testing.T) {
	encoded := fixtureImage(t, "png", 8, 8, 1)
	for _, tc := range []struct{ name, block, prefix, suffix, path, kind string }{
		{"responses", `{"type":"input_image","image_url":"` + dataURL("png", encoded) + `"}`, `{"input":[{"role":"user","content":[`, `]}]}`, "input.0.content", "input_text"},
		{"responses-tool", `{"type":"input_image","image_url":"` + dataURL("png", encoded) + `"}`, `{"input":[{"type":"function_call_output","call_id":"keep-call-id","output":[`, `]}]}`, "input.0.output", "input_text"},
		{"responses-custom-tool", `{"type":"input_image","image_url":"` + dataURL("png", encoded) + `"}`, `{"input":[{"type":"custom_tool_call_output","call_id":"keep-call-id","output":[`, `]}]}`, "input.0.output", "input_text"},
		{"claude", `{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` + encoded + `"}}`, `{"messages":[{"role":"user","content":[`, `]}]}`, "messages.0.content", "text"},
		{"claude-tool", `{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` + encoded + `"}}`, `{"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"keep-call-id","content":[`, `]}]}]}`, "messages.0.content.0.content", "text"},
		{"chat", `{"type":"image_url","image_url":{"url":"` + dataURL("png", encoded) + `"}}`, `{"messages":[{"role":"user","content":[`, `]}]}`, "messages.0.content", "text"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			first := strings.TrimSuffix(tc.block, "}") + `,"cache_control": { "type" : "ephemeral" }} `
			body := []byte(tc.prefix + first + "," + strings.TrimSuffix(strings.Repeat(tc.block+",", 80), ",") + tc.suffix)
			out, err := fixtureGuard(t).Rewrite(body)
			if err != nil || !json.Valid(out) {
				t.Fatalf("invalid rewritten history: %v", err)
			}
			before, after := gjson.GetBytes(body, tc.path).Array(), gjson.GetBytes(out, tc.path).Array()
			if len(before) != len(after) || len(after) != 81 {
				t.Fatal("image block positions changed")
			}
			restored := bytes.Clone(out)
			for i := len(after) - 1; i >= 0; i-- {
				if i >= 41 {
					if before[i].Raw != after[i].Raw {
						t.Fatal("retained image bytes changed")
					}
					continue
				}
				if after[i].Get("type").Str != tc.kind || after[i].Get("text").Str != omittedImage {
					t.Fatal("wrong placeholder type or text")
				}
				if before[i].Get("cache_control").Raw != after[i].Get("cache_control").Raw {
					t.Fatal("cache marker changed")
				}
				start, end := after[i].Index, after[i].Index+len(after[i].Raw)
				restored = append(append(bytes.Clone(restored[:start]), before[i].Raw...), restored[end:]...)
			}
			if !bytes.Equal(restored, body) {
				t.Fatal("bytes outside evicted image blocks changed")
			}
		})
	}
}

func TestHistoryDefaultByteBudget(t *testing.T) {
	block := `{"type":"input_image","image_url":"` + dataURL("png", fixturePaddedImage(t, 1<<20)) + `"}`
	body := fixtureImageHistory(block, 65, "large-history")
	g := fixtureGuard(t)
	out, err := g.Rewrite(body)
	if err != nil {
		t.Fatal(err)
	}
	if len(body) <= 64<<20 || len(out) > imageBytesHigh+(1<<20) {
		t.Fatalf("history did not fit the image budget: before=%d after=%d", len(body), len(out))
	}
	retained := 0
	for _, value := range gjson.GetBytes(out, "input.0.content").Array() {
		if value.Get("type").Str == "input_image" {
			retained++
			if value.Raw != block {
				t.Fatal("byte budget changed retained image data")
			}
		}
	}
	if retained == 0 || retained >= 20 {
		t.Fatalf("unexpected retained image count: %d", retained)
	}
	if cold, err := fixtureGuard(t).Rewrite(body); err != nil || !bytes.Equal(out, cold) {
		t.Fatalf("cold byte-budget replay changed: %v", err)
	}
}

func TestConcurrentHistoryRequestsHaveIndependentCutoffs(t *testing.T) {
	g := fixtureGuard(t)
	block := `{"type":"input_image","image_url":"https://example.invalid/screenshot.png"}`
	var wg sync.WaitGroup
	for _, count := range []int{79, 81, 110, 122, 79, 81, 122} {
		wg.Go(func() {
			body := fixtureImageHistory(block, count, "concurrent")
			warm, err := g.Rewrite(body)
			if err != nil {
				t.Error(err)
				return
			}
			cold, err := fixtureGuard(t).Rewrite(body)
			if err != nil || !bytes.Equal(warm, cold) {
				t.Errorf("another conversation moved the cutoff for %d images: %v", count, err)
			}
		})
	}
	wg.Wait()
}

func TestRequiredComputerScreenshotsAreNotReplacedWithInvalidText(t *testing.T) {
	images := make([]historyImage, 81)
	for i := range images {
		images[i] = historyImage{bytes: 1024, protected: true}
	}
	if _, err := imageEvictions(images); err == nil {
		t.Fatal("required screenshots must not be silently removed")
	}
	images[0].protected = false
	evicted, err := imageEvictions(images)
	if err != nil || !evicted[0] || evicted[80] {
		t.Fatal("eligible history must be evicted without removing required screenshots")
	}
}

func TestHistoryCutoffSurvivesImageCacheEviction(t *testing.T) {
	blocks := []string{
		`{"type":"input_image","image_url":"` + dataURL("png", fixtureImage(t, "png", 384, 24, 1)) + `"}`,
		`{"type":"input_image","image_url":"` + dataURL("png", fixtureImage(t, "png", 384, 24, 2)) + `"}`,
	}
	var history []string
	for i := range 81 {
		history = append(history, blocks[i%2])
	}
	body := []byte(`{"input":[{"role":"user","content":[` + strings.Join(history, ",") + `]}]}`)
	want, err := fixtureGuard(t).Rewrite(body)
	if err != nil {
		t.Fatal(err)
	}
	cfg := DefaultConfig()
	cfg.MaxEdge, cfg.CacheEntries = 256, 1
	g, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	got, err := g.Rewrite(body)
	if err != nil || !bytes.Equal(got, want) || len(g.entries) != 1 {
		t.Fatalf("image-cache eviction changed history or retained bytes: %v", err)
	}
}
