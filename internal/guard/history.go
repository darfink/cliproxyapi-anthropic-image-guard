package guard

import (
	"errors"
	"sort"

	"github.com/tidwall/gjson"
)

const (
	// Leave about 11 MB of Anthropic's 32 MB request limit for text, tools,
	// translation overhead and documents. This is not a token/context limit.
	imageBytesHigh = 20 << 20
	imageBytesLow  = 10 << 20
	imageCountHigh = 80
	imageCountLow  = 40
	omittedImage   = "[Earlier image omitted to keep this request within image limits.]"
)

type historyImage struct {
	block               gjson.Result
	bytes               int
	firstEdit, lastEdit int
	// A computer screenshot occupies a required output object, not a content
	// array. Keep it rather than substitute an invalid computer-tool response.
	protected bool
}

// Replay every original image in order, including already-omitted history. A
// batch drops usage to the low watermarks, then the cutoff stays fixed until a
// high watermark is crossed again. No session IDs, mutable cutoffs, clock or
// cache state can move that boundary between identical or appended requests.
func imageEvictions(images []historyImage) ([]bool, error) {
	evicted := make([]bool, len(images))
	oldest, count, size := 0, 0, 0
	for i, image := range images {
		count++
		size += image.bytes
		if count <= imageCountHigh && size <= imageBytesHigh {
			continue
		}
		// Always keep the newest image. Very large or protected screenshots can
		// leave less headroom, but must not cause silent loss of the latest view.
		for oldest < i && (count > imageCountLow || size > imageBytesLow) {
			if !images[oldest].protected {
				evicted[oldest] = true
				count--
				size -= images[oldest].bytes
			}
			oldest++
		}
		if count > imageCountHigh || size > imageBytesHigh {
			return nil, errors.New("image history cannot fit while preserving the newest image and required computer screenshots; compact the conversation")
		}
	}
	return evicted, nil
}

func imageHistoryEdits(body []byte, images []historyImage, resized []edit) ([]edit, error) {
	if len(images) == 0 {
		return resized, nil
	}
	sort.Slice(images, func(i, j int) bool { return images[i].block.Index < images[j].block.Index })
	evicted, err := imageEvictions(images)
	if err != nil {
		return nil, err
	}
	var edits []edit
	for i, image := range images {
		if !evicted[i] {
			edits = append(edits, resized[image.firstEdit:image.lastEdit]...)
			continue
		}
		kind := "text"
		if image.block.Get("type").Str == "input_image" {
			kind = "input_text"
		}
		value := []byte(`{"type":"` + kind + `","text":"` + omittedImage + `"`)
		// Keep cache markers on the replacement block and preserve their raw JSON.
		for _, key := range []string{"cache_control", "prompt_cache_breakpoint"} {
			if marker := image.block.Get(key); marker.Exists() {
				value = append(value, []byte(`,"`+key+`":`+marker.Raw)...)
			}
		}
		value = append(value, '}')
		change, err := replacementEdit(body, image.block, value)
		if err != nil {
			return nil, err
		}
		edits = append(edits, change)
	}
	return edits, nil
}
