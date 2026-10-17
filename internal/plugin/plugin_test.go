package plugin

import (
	"encoding/json"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func rpc(t *testing.T, m *Manager, method string, req any) pluginabi.Envelope {
	t.Helper()
	encoded, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	var result pluginabi.Envelope
	if err := json.Unmarshal(m.Handle(method, encoded), &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func register(t *testing.T, m *Manager, config string) pluginabi.Envelope {
	t.Helper()
	return rpc(t, m, pluginabi.MethodPluginRegister, map[string]any{"config_yaml": []byte(config)})
}

func intercept(t *testing.T, m *Manager, req pluginapi.RequestInterceptRequest) pluginapi.RequestInterceptResponse {
	t.Helper()
	result := rpc(t, m, pluginabi.MethodRequestInterceptAfter, req)
	if !result.OK {
		t.Fatalf("unexpected RPC error: %v", result.Error)
	}
	var response pluginapi.RequestInterceptResponse
	if err := json.Unmarshal(result.Result, &response); err != nil {
		t.Fatal(err)
	}
	return response
}

func TestRegistrationAndReconfigure(t *testing.T) {
	m := New()
	result := register(t, m, "")
	if !result.OK {
		t.Fatal(result.Error)
	}
	var registration struct {
		SchemaVersion int                `json:"schema_version"`
		Metadata      pluginapi.Metadata `json:"metadata"`
		Capabilities  map[string]bool    `json:"capabilities"`
	}
	if err := json.Unmarshal(result.Result, &registration); err != nil {
		t.Fatal(err)
	}
	if registration.Metadata.Name != ID || registration.SchemaVersion < 2 || len(registration.Capabilities) != 1 || !registration.Capabilities["request_interceptor"] {
		t.Fatalf("unexpected capabilities: %+v", registration)
	}
	original := m.guard
	if !register(t, m, "").OK || m.guard != original {
		t.Fatal("identical reconfigure clears cache")
	}
	if !register(t, m, "max-edge: 1536\n").OK || m.guard == original {
		t.Fatal("changed policy did not replace guard")
	}
	current := m.guard
	for _, cfg := range []string{"max-edge: 2048\n", "unknown-field: true\n", "cache-mib: -1\n"} {
		if register(t, m, cfg).OK || m.guard != current {
			t.Fatal("invalid config changed active guard")
		}
	}
}

func TestScopeAndNoOp(t *testing.T) {
	m := New()
	if !register(t, m, "").OK {
		t.Fatal("registration failed")
	}
	for _, format := range []string{"openai-response", "codex", "gemini", "antigravity", ""} {
		out := intercept(t, m, pluginapi.RequestInterceptRequest{ToFormat: format, Body: []byte("not JSON")})
		if out.Terminate || out.Body != nil || len(out.Headers) != 0 {
			t.Fatalf("unrelated provider changed: %s", format)
		}
	}
	for _, stream := range []bool{false, true} {
		out := intercept(t, m, pluginapi.RequestInterceptRequest{ToFormat: "claude", Stream: stream, Body: []byte(`{"input":"text only","cache_control":{"type":"ephemeral"}}`)})
		if out.Terminate || out.Body != nil || len(out.Headers) != 0 {
			t.Fatal("no-op must not replace body or headers")
		}
	}
	before := rpc(t, m, pluginabi.MethodRequestInterceptBefore, map[string]any{"Body": []byte("anything")})
	if !before.OK || string(before.Result) == "null" {
		t.Fatal("before-auth must be a no-op response")
	}
}

func TestUnsafeImageTerminatesInsteadOfFailingOpen(t *testing.T) {
	m := New()
	if !register(t, m, "").OK {
		t.Fatal("registration failed")
	}
	for _, stream := range []bool{false, true} {
		out := intercept(t, m, pluginapi.RequestInterceptRequest{
			ToFormat: "claude", SourceFormat: "openai-response", Stream: stream,
			Body: []byte(`{"input":[{"role":"user","content":[{"type":"input_image","image_url":"data:image/png;base64,%%%"}]}]}`),
		})
		if !out.Terminate || out.StatusCode != 400 || out.Body != nil || !json.Valid(out.ResponseBody) {
			t.Fatal("unsafe image must return an explicit safe rejection")
		}
	}
}

func TestDisabledAndShutdown(t *testing.T) {
	m := New()
	if !register(t, m, "enabled: false\npriority: 10\n").OK {
		t.Fatal("registration failed")
	}
	out := intercept(t, m, pluginapi.RequestInterceptRequest{ToFormat: "claude", Body: []byte("invalid JSON")})
	if out.Terminate || out.Body != nil {
		t.Fatal("disabled plugin is not a no-op")
	}
	if !rpc(t, m, pluginabi.MethodPluginShutdown, nil).OK || m.guard != nil {
		t.Fatal("shutdown did not release cache")
	}
}

func TestMalformedRPC(t *testing.T) {
	for _, method := range []string{pluginabi.MethodPluginRegister, pluginabi.MethodRequestInterceptAfter, "unknown"} {
		var out pluginabi.Envelope
		if err := json.Unmarshal(New().Handle(method, []byte("{invalid")), &out); err != nil {
			t.Fatal(err)
		}
		if out.OK || out.Error == nil {
			t.Fatal("invalid RPC must fail safely")
		}
	}
}
