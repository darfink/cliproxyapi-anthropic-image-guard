// Package guard normalizes inline images without changing surrounding request bytes.
package guard

import (
	"bytes"
	"container/list"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"io"
	"sort"
	"strings"
	"sync"

	"github.com/disintegration/imaging"
	"github.com/tidwall/gjson"
	_ "golang.org/x/image/webp"
)

// Config controls image dimensions and bounded in-process resources.
type Config struct {
	MaxEdge         int   `yaml:"max-edge"`
	CacheMiB        int   `yaml:"cache-mib"`
	CacheEntries    int   `yaml:"cache-entries"`
	MaxDecodePixels int64 `yaml:"max-decode-pixels"`
	// Enabled and Priority are host-owned plugin configuration fields.
	Enabled  bool `yaml:"enabled"`
	Priority int  `yaml:"priority"`
}

func DefaultConfig() Config {
	return Config{MaxEdge: 2000, CacheMiB: 64, CacheEntries: 256, MaxDecodePixels: 32_000_000, Enabled: true}
}

func (c Config) Validate() error {
	if c.MaxEdge < 256 || c.MaxEdge > 2000 {
		return errors.New("max-edge must be between 256 and 2000")
	}
	if c.CacheMiB < 0 || c.CacheMiB > 256 || c.CacheEntries < 0 || c.CacheEntries > 4096 {
		return errors.New("cache-mib must be 0..256 and cache-entries must be 0..4096")
	}
	if c.MaxDecodePixels < 4_000_000 || c.MaxDecodePixels > 64_000_000 {
		return errors.New("max-decode-pixels must be between 4000000 and 64000000")
	}
	return nil
}

type normalized struct {
	data    string
	format  string
	changed bool
}

type cacheEntry struct {
	key   [32]byte
	value normalized
	bytes int
}

// Guard owns a byte-bounded LRU. One worker bounds concurrent image decoding.
// Eviction changes CPU use only: image encoding never depends on cache state.
type Guard struct {
	cfg        Config
	mu         sync.Mutex
	lru        *list.List
	entries    map[[32]byte]*list.Element
	cacheBytes int
	worker     chan struct{}
}

func New(cfg Config) (*Guard, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &Guard{cfg: cfg, lru: list.New(), entries: make(map[[32]byte]*list.Element), worker: make(chan struct{}, 1)}, nil
}

type edit struct {
	start, end int
	value      []byte
}

func replacementEdit(body []byte, v gjson.Result, value []byte) (edit, error) {
	if v.Index < 0 || v.Index+len(v.Raw) > len(body) || !bytes.Equal(body[v.Index:v.Index+len(v.Raw)], []byte(v.Raw)) {
		return edit{}, errors.New("image guard could not locate an image field safely")
	}
	return edit{v.Index, v.Index + len(v.Raw), value}, nil
}

// Rewrite returns nil when no image changes. Retained images keep their encoded
// bytes; eviction replaces whole image blocks with stable text placeholders.
// All bytes outside those blocks and resized image fields stay intact.
func (g *Guard) Rewrite(body []byte) ([]byte, error) {
	if !gjson.ValidBytes(body) {
		return nil, errors.New("image guard requires a valid JSON request")
	}
	var edits []edit
	var images []historyImage
	var visitErr error
	add := func(v gjson.Result, s string) {
		if visitErr != nil {
			return
		}
		if v.Type != gjson.String {
			visitErr = errors.New("image guard could not locate an image field safely")
			return
		}
		encoded, _ := json.Marshal(s)
		change, err := replacementEdit(body, v, encoded)
		if err != nil {
			visitErr = err
			return
		}
		edits = append(edits, change)
	}
	visit := func(block gjson.Result) {
		if visitErr != nil {
			return
		}
		image := historyImage{block: block, bytes: len(block.Raw), firstEdit: len(edits), protected: block.Get("type").Str == "computer_screenshot"}
		defer func() {
			if visitErr == nil {
				image.lastEdit = len(edits)
				for _, e := range edits[image.firstEdit:image.lastEdit] {
					image.bytes += len(e.value) - (e.end - e.start)
				}
				images = append(images, image)
			}
		}()
		var data, media gjson.Result
		isURL := false
		switch block.Get("type").Str {
		case "image":
			if block.Get("source.type").Str != "base64" {
				return
			}
			data, media = block.Get("source.data"), block.Get("source.media_type")
		case "input_image", "computer_screenshot":
			data, isURL = block.Get("image_url"), true
		case "image_url":
			data, isURL = block.Get("image_url.url"), true
		default:
			return
		}
		if data.Type != gjson.String {
			return
		}
		encoded := data.Str
		if isURL {
			if !strings.HasPrefix(encoded, "data:image/") {
				return
			}
			comma := strings.IndexByte(encoded, ',')
			if comma < 0 || !strings.HasSuffix(encoded[:comma], ";base64") {
				visitErr = errors.New("image guard requires base64 data URLs")
				return
			}
			encoded = encoded[comma+1:]
		}
		result, err := g.normalize(encoded)
		if err != nil {
			visitErr = err
			return
		}
		if !result.changed {
			return
		}
		if isURL {
			add(data, "data:image/"+result.format+";base64,"+result.data)
		} else {
			if media.Type != gjson.String {
				visitErr = errors.New("image guard requires an image media_type")
				return
			}
			add(data, result.data)
			if media.Str != "image/"+result.format {
				add(media, "image/"+result.format)
			}
		}
	}
	// Walk only protocol content containers, never tool arguments, schemas,
	// arbitrary JSON strings or encrypted reasoning blobs.
	var walk func(gjson.Result, int)
	walk = func(v gjson.Result, depth int) {
		if visitErr != nil {
			return
		}
		if depth > 32 {
			visitErr = errors.New("image content nesting exceeds the processing limit")
			return
		}
		if v.IsArray() {
			v.ForEach(func(_, item gjson.Result) bool { walk(item, depth+1); return visitErr == nil })
			return
		}
		if !v.IsObject() {
			return
		}
		switch v.Get("type").Str {
		case "image", "input_image", "image_url", "computer_screenshot":
			visit(v)
		case "tool_result", "function_call_output", "custom_tool_call_output", "computer_call_output":
			walk(v.Get("content"), depth+1)
			walk(v.Get("output"), depth+1)
		case "message", "":
			if v.Get("role").Exists() {
				walk(v.Get("content"), depth+1)
			}
		}
	}
	// GetBytes copies only the selected container, not unrelated tools or metadata.
	// Bound individual images below, not the size of the surrounding conversation.
	walk(gjson.GetBytes(body, "input"), 0)
	walk(gjson.GetBytes(body, "messages"), 0)
	if visitErr != nil {
		return nil, visitErr
	}
	edits, err := imageHistoryEdits(body, images, edits)
	if err != nil {
		return nil, err
	}
	if len(edits) == 0 {
		return nil, nil
	}
	sort.Slice(edits, func(i, j int) bool { return edits[i].start < edits[j].start })
	size := len(body)
	for _, e := range edits {
		size += len(e.value) - (e.end - e.start)
	}
	out := make([]byte, 0, size)
	position := 0
	for _, e := range edits {
		if e.start < position {
			return nil, errors.New("image field edits overlap")
		}
		out = append(out, body[position:e.start]...)
		out = append(out, e.value...)
		position = e.end
	}
	out = append(out, body[position:]...)
	return out, nil
}

func (g *Guard) cached(key [32]byte) (normalized, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if element := g.entries[key]; element != nil {
		g.lru.MoveToFront(element)
		return element.Value.(cacheEntry).value, true
	}
	return normalized{}, false
}

func (g *Guard) remember(key [32]byte, value normalized) {
	// Keys, list nodes and map entries also consume memory, including no-op entries.
	cost := len(value.data) + 256
	budget := g.cfg.CacheMiB << 20
	if g.cfg.CacheEntries == 0 || cost > budget {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	for g.cacheBytes+cost > budget || len(g.entries) >= g.cfg.CacheEntries {
		last := g.lru.Back()
		entry := last.Value.(cacheEntry)
		delete(g.entries, entry.key)
		g.cacheBytes -= entry.bytes
		g.lru.Remove(last)
	}
	g.entries[key] = g.lru.PushFront(cacheEntry{key, value, cost})
	g.cacheBytes += cost
}

func (g *Guard) normalize(encoded string) (normalized, error) {
	key := sha256.Sum256([]byte(encoded))
	if value, ok := g.cached(key); ok {
		return value, nil
	}
	// Recheck after acquiring the worker: concurrent copies of the same image
	// share one decode/encode instead of multiplying transient memory.
	g.worker <- struct{}{}
	defer func() { <-g.worker }()
	if value, ok := g.cached(key); ok {
		return value, nil
	}
	if len(encoded) > 16<<20 {
		return normalized{}, errors.New("inline image exceeds the 16 MiB base64 processing limit")
	}
	// NewDecoder validates each chunk separately. Reject data after padding even
	// when the padding falls at a chunk boundary, as DecodeString did in v0.1.0.
	if pad := strings.IndexByte(encoded, '='); pad >= 0 && strings.Trim(encoded[pad:], "=\r\n") != "" {
		return normalized{}, errors.New("inline image has invalid base64 data")
	}
	reader := base64.NewDecoder(base64.StdEncoding, strings.NewReader(encoded))
	config, format, headerErr := image.DecodeConfig(reader)
	// Header decoding can stop early. Validate the entire base64 stream, including
	// trailing data, without retaining a second full copy of the image.
	_, err := io.Copy(io.Discard, reader)
	var corrupt base64.CorruptInputError
	if err != nil || errors.As(headerErr, &corrupt) {
		return normalized{}, errors.New("inline image has invalid base64 data")
	}
	if headerErr != nil {
		return normalized{}, errors.New("inline image has an unsupported or invalid image header")
	}
	if config.Width <= g.cfg.MaxEdge && config.Height <= g.cfg.MaxEdge {
		value := normalized{}
		g.remember(key, value)
		return value, nil
	}
	if config.Width <= 0 || config.Height <= 0 || int64(config.Width) > g.cfg.MaxDecodePixels/int64(config.Height) {
		return normalized{}, fmt.Errorf("image exceeds the %d-pixel decode budget; crop or resize it first", g.cfg.MaxDecodePixels)
	}
	// Apply EXIF orientation before re-encoding, which removes EXIF metadata.
	reader = base64.NewDecoder(base64.StdEncoding, strings.NewReader(encoded))
	decoded, err := imaging.Decode(reader, imaging.AutoOrientation(true))
	if err != nil {
		return normalized{}, errors.New("inline image could not be decoded safely")
	}
	resized := imaging.Fit(decoded, g.cfg.MaxEdge, g.cfg.MaxEdge, imaging.Lanczos)
	var output bytes.Buffer
	if format == "jpeg" {
		err = jpeg.Encode(&output, resized, &jpeg.Options{Quality: 90})
	} else {
		// Preserve transparency. Claude reads the first frame of GIF images only.
		format = "png"
		encoder := png.Encoder{CompressionLevel: png.DefaultCompression}
		err = encoder.Encode(&output, resized)
	}
	if err != nil {
		return normalized{}, errors.New("image encoding failed")
	}
	if output.Len() > 7_000_000 {
		return normalized{}, errors.New("resized image exceeds the safe upload size; crop or compress it first")
	}
	value := normalized{data: base64.StdEncoding.EncodeToString(output.Bytes()), format: format, changed: true}
	g.remember(key, value)
	return value, nil
}
