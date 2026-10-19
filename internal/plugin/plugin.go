// Package plugin implements the native plugin RPC contract without model or auth ownership.
package plugin

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	"gopkg.in/yaml.v3"

	"github.com/darfink/cliproxyapi-anthropic-image-guard/internal/guard"
)

const ID = "anthropic-image-guard"

type Manager struct {
	mu    sync.RWMutex
	cfg   guard.Config
	guard *guard.Guard
}

func New() *Manager { return &Manager{} }

func envelope(result any, callError *pluginabi.Error) []byte {
	value := pluginabi.Envelope{OK: callError == nil, Error: callError}
	if callError == nil {
		value.Result, _ = json.Marshal(result)
	}
	encoded, _ := json.Marshal(value)
	return encoded
}

// Handle serves only registration and request interception. No response or
// streaming callbacks are registered, so model output cannot be modified.
func (m *Manager) Handle(method string, raw []byte) (out []byte) {
	defer func() {
		if recover() != nil {
			// Never log image bytes, prompts, credentials or recovered values.
			out = envelope(nil, pluginabi.NewError("plugin_error", "image guard encountered an internal error"))
		}
	}()
	switch method {
	case pluginabi.MethodPluginRegister, pluginabi.MethodPluginReconfigure:
		var req struct {
			ConfigYAML []byte `json:"config_yaml"`
		}
		if json.Unmarshal(raw, &req) != nil {
			return envelope(nil, pluginabi.NewError("invalid_request", "invalid lifecycle request"))
		}
		cfg := guard.DefaultConfig()
		if len(bytes.TrimSpace(req.ConfigYAML)) > 0 {
			decoder := yaml.NewDecoder(bytes.NewReader(req.ConfigYAML))
			decoder.KnownFields(true)
			if decoder.Decode(&cfg) != nil {
				return envelope(nil, pluginabi.NewError("invalid_config", "invalid image guard configuration"))
			}
		}
		g, err := guard.New(cfg)
		if err != nil {
			return envelope(nil, pluginabi.NewError("invalid_config", err.Error()))
		}
		m.mu.Lock()
		if m.guard == nil || m.cfg != cfg {
			m.guard, m.cfg = g, cfg
		}
		m.mu.Unlock()
		return envelope(map[string]any{
			"schema_version": pluginabi.SchemaVersion,
			"metadata": pluginapi.Metadata{
				Name: ID, Version: "0.2.0", Author: "darfink",
				GitHubRepository: "https://github.com/darfink/cliproxyapi-anthropic-image-guard",
				ConfigFields: []pluginapi.ConfigField{
					{Name: "max-edge", Type: pluginapi.ConfigFieldTypeInteger, Description: "Maximum image edge in pixels. Default: 2000. Range: 256..2000."},
					{Name: "cache-mib", Type: pluginapi.ConfigFieldTypeInteger, Description: "In-memory cache budget in MiB. Default: 64. Zero disables the cache."},
					{Name: "cache-entries", Type: pluginapi.ConfigFieldTypeInteger, Description: "Maximum cached image results. Default: 256."},
					{Name: "max-decode-pixels", Type: pluginapi.ConfigFieldTypeInteger, Description: "Maximum decoded image pixels. Default: 32000000."},
				},
			},
			"capabilities": map[string]bool{"request_interceptor": true},
		}, nil)
	case pluginabi.MethodRequestInterceptBefore:
		return envelope(pluginapi.RequestInterceptResponse{}, nil)
	case pluginabi.MethodRequestInterceptAfter:
		var req pluginapi.RequestInterceptRequest
		if json.Unmarshal(raw, &req) != nil {
			return envelope(nil, pluginabi.NewError("invalid_request", "invalid interception request"))
		}
		// After-auth still carries the client protocol body. ToFormat identifies
		// Claude even when the request uses a model alias or a pooled credential.
		if !strings.EqualFold(req.ToFormat, "claude") {
			return envelope(pluginapi.RequestInterceptResponse{}, nil)
		}
		m.mu.RLock()
		g, enabled := m.guard, m.cfg.Enabled
		m.mu.RUnlock()
		if g == nil || !enabled {
			return envelope(pluginapi.RequestInterceptResponse{}, nil)
		}
		body, err := g.Rewrite(req.Body)
		if err != nil {
			// An RPC error is fail-open in the host. Terminate explicitly when an
			// image cannot be repaired within the resource limits; do not drop it.
			message, _ := json.Marshal(map[string]any{"error": map[string]string{"type": "invalid_request_error", "message": fmt.Sprintf("Anthropic image guard: %s", err)}})
			return envelope(pluginapi.RequestInterceptResponse{Terminate: true, StatusCode: http.StatusBadRequest,
				ResponseHeaders: http.Header{"Content-Type": {"application/json"}}, ResponseBody: message}, nil)
		}
		return envelope(pluginapi.RequestInterceptResponse{Body: body}, nil)
	case pluginabi.MethodPluginQuiesce, pluginabi.MethodPluginShutdown:
		m.mu.Lock()
		m.guard = nil
		m.mu.Unlock()
		return envelope(map[string]any{}, nil)
	default:
		return envelope(nil, pluginabi.NewError("unsupported", "unsupported image guard method"))
	}
}
