package main

import (
	"fmt"
	"strings"
	"testing"

	"github.com/MoeForge/moego/json"
	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func TestABIToolChoiceBoundaries(t *testing.T) {
	for _, test := range []struct {
		name      string
		choice    any
		parallel  bool
		text      string
		wantCalls int
		wantError bool
	}{
		{"auto-prose", "auto", true, "No tool needed.", 0, false},
		{"none-prose", "none", true, "No tool needed.", 0, false},
		{"none-rejects-call", "none", true, nativeWeatherText, 0, true},
		{"required-rejects-prose", "required", true, "No tool needed.", 0, true},
		{"required-call", "required", true, nativeWeatherText, 1, false},
		{"named-call", map[string]any{"type": "function", "function": map[string]any{"name": "get_weather"}}, true, nativeWeatherText, 1, false},
		{"named-rejects-other", map[string]any{"type": "function", "function": map[string]any{"name": "get_time"}}, true, nativeWeatherText, 0, true},
		{"parallel-two-calls", "auto", true, nativeWeatherText + "\n" + nativeWeatherText, 2, false},
		{"serial-rejects-two-calls", "auto", false, nativeWeatherText + "\n" + nativeWeatherText, 0, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			p := NewPlugin(func(method string, raw []byte) ([]byte, error) {
				if method != pluginabi.MethodHostModelExecute {
					return nil, fmt.Errorf("unexpected host method %s", method)
				}
				return json.Marshal(map[string]any{"ok": true, "result": pluginapi.HostModelExecutionResponse{StatusCode: 200, Body: nativeChatBody(test.text)}})
			})
			registerTest(t, p, "parse_retry: 0\n")
			doc := ojson.NewDoc([]byte(chatWeatherRequest))
			if errSet := doc.Set("tool_choice", test.choice); errSet != nil {
				t.Fatal(errSet)
			}
			if errSet := doc.Set("parallel_tool_calls", test.parallel); errSet != nil {
				t.Fatal(errSet)
			}
			body, errHandle := p.Handle(pluginabi.MethodExecutorExecute, marshalTest(t, map[string]any{"SourceFormat": "openai", "Model": "test-model", "OriginalRequest": doc.Bytes(), "host_callback_id": "lease"}))
			if errHandle != nil {
				t.Fatal(errHandle)
			}
			envelope := ojson.NewDoc(body)
			if test.wantError {
				if envelope.Get("ok").Bool() || envelope.Get("error.http_status").Int() != 502 {
					t.Fatalf("tool-choice violation was accepted: %s", body)
				}
				return
			}
			if !envelope.Get("ok").Bool() {
				t.Fatalf("valid tool choice was rejected: %s", body)
			}
			var response pluginapi.ExecutorResponse
			if errDecode := json.Unmarshal([]byte(envelope.Get("result").Raw), &response); errDecode != nil {
				t.Fatal(errDecode)
			}
			if count := len(ojson.NewDoc(response.Payload).Get("choices.0.message.tool_calls").Array()); count != test.wantCalls {
				t.Fatalf("got %d calls, want %d: %s", count, test.wantCalls, response.Payload)
			}
		})
	}
}

func TestABIStringArgumentsPreserveBoundaryCharacters(t *testing.T) {
	for _, value := range []string{"東京 | Osaka", `quote " and backslash \`, "first\nsecond", "  padded value  ", "", "<PRIVATE_0123456789abcdef>", "null", "[1,2]"} {
		t.Run(fmt.Sprintf("value-%q", value), func(t *testing.T) {
			p := NewPlugin(func(method string, raw []byte) ([]byte, error) {
				if method != pluginabi.MethodHostModelExecute {
					return nil, fmt.Errorf("unexpected host method %s", method)
				}
				return json.Marshal(map[string]any{"ok": true, "result": pluginapi.HostModelExecutionResponse{StatusCode: 200, Body: nativeChatBody(strings.ReplaceAll(nativeWeatherText, "Tokyo", value))}})
			})
			registerTest(t, p, "parse_retry: 0\n")
			result := callTest(t, p, pluginabi.MethodExecutorExecute, map[string]any{"SourceFormat": "openai", "Model": "test-model", "OriginalRequest": []byte(chatWeatherRequest), "host_callback_id": "lease"})
			var response pluginapi.ExecutorResponse
			if errDecode := json.Unmarshal(result, &response); errDecode != nil {
				t.Fatal(errDecode)
			}
			arguments := ojson.NewDoc(response.Payload).Get("choices.0.message.tool_calls.0.function.arguments").String()
			var parsed map[string]string
			if errDecode := json.Unmarshal([]byte(arguments), &parsed); errDecode != nil {
				t.Fatal(errDecode)
			}
			if got, exists := parsed["city"]; !exists || got != value {
				t.Fatalf("string argument changed: got %q, want %q", arguments, value)
			}
		})
	}
}
