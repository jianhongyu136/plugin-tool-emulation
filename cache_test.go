package main

import (
	"fmt"
	"testing"

	"github.com/MoeForge/moego/json"
	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func TestABIPreservesToolPrefixAcrossChoiceAndHistoryChanges(t *testing.T) {
	var prefixes []string
	p := NewPlugin(func(method string, raw []byte) ([]byte, error) {
		if method != pluginabi.MethodHostModelExecute {
			return nil, fmt.Errorf("unexpected host method %s", method)
		}
		var request pluginapi.HostModelExecutionRequest
		if errDecode := json.Unmarshal(raw, &request); errDecode != nil {
			return nil, errDecode
		}
		doc := ojson.NewDoc(request.Body)
		prefixes = append(prefixes, doc.Get("messages.0.content.0.text").String())
		return json.Marshal(map[string]any{"ok": true, "result": pluginapi.HostModelExecutionResponse{StatusCode: 200, Body: nativeChatBody(nativeWeatherText)}})
	})
	registerTest(t, p, "")
	first := ojson.NewDoc([]byte(chatWeatherRequest))
	second := ojson.NewDoc([]byte(chatWeatherRequest))
	if errSet := second.Set("tool_choice", "auto"); errSet != nil {
		t.Fatal(errSet)
	}
	if errSet := second.Set("messages.-1", map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"id": "call-history", "type": "function", "function": map[string]any{"name": "get_weather", "arguments": `{"city":"Tokyo"}`}}}}); errSet != nil {
		t.Fatal(errSet)
	}
	if errSet := second.Set("messages.-1", map[string]any{"role": "tool", "tool_call_id": "call-history", "content": "rain"}); errSet != nil {
		t.Fatal(errSet)
	}
	if errSet := second.Set("messages.-1", map[string]any{"role": "user", "content": "tomorrow?"}); errSet != nil {
		t.Fatal(errSet)
	}
	for _, body := range [][]byte{first.Bytes(), second.Bytes()} {
		callTest(t, p, pluginabi.MethodExecutorExecute, map[string]any{"SourceFormat": "openai", "Model": "test-model", "OriginalRequest": body, "host_callback_id": "lease"})
	}
	if len(prefixes) != 2 || prefixes[0] == "" || prefixes[0] != prefixes[1] {
		t.Fatalf("tool prefix changed across turns: lengths=%d/%d", len(prefixes[0]), len(prefixes[1]))
	}
}

func TestABIParallelLimitDoesNotChangeCachedToolPrefix(t *testing.T) {
	var prefixes []string
	p := NewPlugin(func(method string, raw []byte) ([]byte, error) {
		if method != pluginabi.MethodHostModelExecute {
			return nil, fmt.Errorf("unexpected host method %s", method)
		}
		var request pluginapi.HostModelExecutionRequest
		if errDecode := json.Unmarshal(raw, &request); errDecode != nil {
			return nil, errDecode
		}
		prefixes = append(prefixes, ojson.NewDoc(request.Body).Get("messages.0.content.0.text").String())
		return json.Marshal(map[string]any{"ok": true, "result": pluginapi.HostModelExecutionResponse{StatusCode: 200, Body: nativeChatBody(nativeWeatherText)}})
	})
	registerTest(t, p, "")
	for _, parallel := range []bool{true, false} {
		doc := ojson.NewDoc([]byte(chatWeatherRequest))
		if errSet := doc.Set("parallel_tool_calls", parallel); errSet != nil {
			t.Fatal(errSet)
		}
		callTest(t, p, pluginabi.MethodExecutorExecute, map[string]any{"SourceFormat": "openai", "Model": "test-model", "OriginalRequest": doc.Bytes(), "host_callback_id": "lease"})
	}
	if len(prefixes) != 2 || prefixes[0] == "" || prefixes[0] != prefixes[1] {
		t.Fatal("per-turn parallel limit changed the cacheable tool protocol prefix")
	}
}
