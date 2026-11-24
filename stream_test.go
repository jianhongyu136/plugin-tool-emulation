package main

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MoeForge/moego/json"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func TestABIStreamsSplitProtocolAsNativeToolCalls(t *testing.T) {
	var mu sync.Mutex
	var emitted []byte
	closed := make(chan string, 1)
	read := false
	p := NewPlugin(func(method string, raw []byte) ([]byte, error) {
		var result any
		switch method {
		case pluginabi.MethodHostModelExecuteStream:
			result = pluginapi.HostModelStreamResponse{StreamID: "upstream", Headers: map[string][]string{"Content-Type": {"text/event-stream"}}}
		case pluginabi.MethodHostModelStreamRead:
			if read {
				result = pluginapi.HostModelStreamReadResponse{Done: true}
				break
			}
			read = true
			var frames strings.Builder
			for _, text := range []string{nativeWeatherText[:6], nativeWeatherText[6:]} {
				body, _ := json.Marshal(map[string]any{"id": "chatcmpl-stream", "model": "test-model", "choices": []any{map[string]any{"index": 0, "delta": map[string]any{"content": text}}}})
				frames.WriteString("data: " + string(body) + "\n\n")
			}
			frames.WriteString("data: {\"id\":\"chatcmpl-stream\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
			result = pluginapi.HostModelStreamReadResponse{Payload: []byte(frames.String())}
		case pluginabi.MethodHostModelStreamClose:
			result = struct{}{}
		case pluginabi.MethodHostStreamEmit:
			var frame struct {
				Payload []byte `json:"payload"`
			}
			if errDecode := json.Unmarshal(raw, &frame); errDecode != nil {
				return nil, errDecode
			}
			mu.Lock()
			emitted = append(emitted, frame.Payload...)
			mu.Unlock()
			result = struct{}{}
		case pluginabi.MethodHostStreamClose:
			var done struct {
				Error string `json:"error"`
			}
			if errDecode := json.Unmarshal(raw, &done); errDecode != nil {
				return nil, errDecode
			}
			closed <- done.Error
			result = struct{}{}
		default:
			return nil, fmt.Errorf("unexpected host method %s", method)
		}
		return json.Marshal(map[string]any{"ok": true, "result": result})
	})
	registerTest(t, p, "")
	callTest(t, p, pluginabi.MethodExecutorExecuteStream, map[string]any{"SourceFormat": "openai", "Model": "test-model", "OriginalRequest": []byte(chatWeatherRequest), "host_callback_id": "lease", "stream_id": "client-stream"})
	select {
	case errorText := <-closed:
		if errorText != "" {
			t.Fatalf("stream failed: %s", errorText)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("stream did not complete")
	}
	mu.Lock()
	output := string(emitted)
	mu.Unlock()
	if !strings.Contains(output, `"tool_calls"`) || !strings.Contains(output, `"get_weather"`) || !strings.Contains(output, "data: [DONE]") || strings.Contains(output, "<CPA_TC") {
		t.Fatalf("not native tool-call SSE: %s", output)
	}
}
