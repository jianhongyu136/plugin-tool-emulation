package main

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/MoeForge/moego/json"
	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

const nativeWeatherText = "<CPA_TC|get_weather|SEED1234>\n<CPA_TA|city|SEED1234>\nTokyo\n</CPA_TA|SEED1234>\n</CPA_TC|SEED1234>"

const chatWeatherRequest = `{"model":"test-model","messages":[{"role":"user","content":"weather?"}],"tools":[{"type":"function","function":{"name":"get_weather","parameters":{"type":"object","properties":{"city":{"type":"string"}}}}}],"tool_choice":"required","prompt_cache_key":"client-session"}`

func marshalTest(t *testing.T, value any) []byte {
	t.Helper()
	raw, errMarshal := json.Marshal(value)
	if errMarshal != nil {
		t.Fatal(errMarshal)
	}
	return raw
}

func callTest(t *testing.T, p *Plugin, method string, request any) []byte {
	t.Helper()
	raw, errCall := p.Handle(method, marshalTest(t, request))
	if errCall != nil {
		t.Fatal(errCall)
	}
	var result pluginabi.Envelope
	if errDecode := json.Unmarshal(raw, &result); errDecode != nil {
		t.Fatal(errDecode)
	}
	if !result.OK {
		t.Fatalf("%s failed: %s", method, raw)
	}
	return result.Result
}

func registerTest(t *testing.T, p *Plugin, config string) {
	t.Helper()
	if !strings.Contains(config, "rules:") {
		config += "\nrules:\n  - models: ['*']\n"
	}
	callTest(t, p, pluginabi.MethodPluginRegister, map[string]any{
		"schema_version": pluginabi.SchemaVersion,
		"config_yaml":    []byte(fmt.Sprintf("enabled: true\n%s: %s\n", "fence_"+"token", "SEED1234") + config),
	})
	t.Cleanup(func() { _, _ = p.Handle(pluginabi.MethodPluginShutdown, []byte(`{}`)) })
}

func nativeChatBody(text string) []byte {
	raw, _ := json.Marshal(map[string]any{
		"id": "chatcmpl-native", "object": "chat.completion", "model": "test-model",
		"choices": []any{map[string]any{"index": 0, "message": map[string]any{"role": "assistant", "content": text}, "finish_reason": "stop"}},
		"usage":   map[string]any{"prompt_tokens": 12, "completion_tokens": 7, "total_tokens": 19, "prompt_tokens_details": map[string]any{"cached_tokens": 4}},
	})
	return raw
}

func TestABIExecutesEmulatedNativeToolCall(t *testing.T) {
	var sent pluginapi.HostModelExecutionRequest
	p := NewPlugin(func(method string, request []byte) ([]byte, error) {
		if method != pluginabi.MethodHostModelExecute {
			return nil, fmt.Errorf("unexpected host method %q", method)
		}
		if errDecode := json.Unmarshal(request, &sent); errDecode != nil {
			return nil, errDecode
		}
		return json.Marshal(map[string]any{"ok": true, "result": pluginapi.HostModelExecutionResponse{
			StatusCode: http.StatusOK, Headers: http.Header{"Content-Type": {"application/json"}}, Body: nativeChatBody(nativeWeatherText),
		}})
	})
	registerTest(t, p, "rules:\n  - provider: test-provider\n    models: [test-model]\n")
	result := callTest(t, p, pluginabi.MethodExecutorExecute, map[string]any{
		"SourceFormat": "openai", "Model": "test-model", "OriginalRequest": []byte(chatWeatherRequest),
		"Payload": []byte(`{"model":"test-model","messages":[],"metadata":{"already_configured":true}}`), "host_callback_id": "lease-1",
	})
	var response pluginapi.ExecutorResponse
	if errDecode := json.Unmarshal(result, &response); errDecode != nil {
		t.Fatal(errDecode)
	}
	got := ojson.NewDoc(response.Payload)
	if got.Get("choices.0.message.tool_calls.0.function.name").String() != "get_weather" || got.Get("choices.0.finish_reason").String() != "tool_calls" {
		t.Fatalf("not a native tool response: %s", response.Payload)
	}
	if args := got.Get("choices.0.message.tool_calls.0.function.arguments").String(); args != `{"city":"Tokyo"}` {
		t.Fatalf("arguments = %q", args)
	}
	if got.Get("usage.prompt_tokens_details.cached_tokens").Int() != 4 {
		t.Fatalf("cache usage was lost: %s", response.Payload)
	}
	upstream := ojson.NewDoc(sent.Body)
	if upstream.Get("tools").Exists() || upstream.Get("tool_choice").Exists() || upstream.Get("metadata.already_configured").Exists() {
		t.Fatalf("native tool fields or the configured outer payload leaked upstream: %s", sent.Body)
	}
	if !strings.Contains(upstream.Get("messages.0.content.0.text").String(), "<tools_doc>") || upstream.Get("prompt_cache_key").String() != "client-session" {
		t.Fatalf("tool protocol or caller cache key missing: %s", sent.Body)
	}
	if sent.ForcedProvider != "test-provider" || sent.ExitProtocol != "openai" {
		t.Fatalf("incorrect native delegation: %+v", sent)
	}
}
