package main

import (
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MoeForge/moego/json"
	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

// runABIStream verifies the dispatcher and stream callbacks without replacing
// any parser, emitter, or executor implementation.
func runABIStream(t *testing.T, body []byte, config string, frames ...[]byte) (string, string, int) {
	t.Helper()
	return runABIStreamFormat(t, "openai", body, config, frames...)
}

func runABIStreamFormat(t *testing.T, format string, body []byte, config string, frames ...[]byte) (string, string, int) {
	t.Helper()
	var mu sync.Mutex
	var output strings.Builder
	var reads atomic.Int32
	closed := make(chan string, 1)
	p := NewPlugin(func(method string, raw []byte) ([]byte, error) {
		var result any = struct{}{}
		switch method {
		case pluginabi.MethodHostModelExecuteStream:
			result = pluginapi.HostModelStreamResponse{StreamID: "regression-upstream"}
		case pluginabi.MethodHostModelStreamRead:
			index := int(reads.Add(1)) - 1
			if index < len(frames) {
				result = pluginapi.HostModelStreamReadResponse{Payload: frames[index]}
			} else {
				result = pluginapi.HostModelStreamReadResponse{Done: true}
			}
		case pluginabi.MethodHostModelStreamClose:
		case pluginabi.MethodHostStreamEmit:
			var frame struct {
				Payload []byte `json:"payload"`
			}
			if err := json.Unmarshal(raw, &frame); err != nil {
				return nil, err
			}
			mu.Lock()
			output.Write(frame.Payload)
			mu.Unlock()
		case pluginabi.MethodHostStreamClose:
			var response struct {
				Error string `json:"error"`
			}
			if err := json.Unmarshal(raw, &response); err != nil {
				return nil, err
			}
			closed <- response.Error
		default:
			return nil, fmt.Errorf("unexpected host callback %s", method)
		}
		return json.Marshal(map[string]any{"ok": true, "result": result})
	})
	registerTest(t, p, config)
	callTest(t, p, pluginabi.MethodExecutorExecuteStream, map[string]any{
		"SourceFormat": format, "Model": "test-model", "OriginalRequest": body,
		"host_callback_id": "regression-lease", "stream_id": "regression-client",
	})
	var closeError string
	select {
	case closeError = <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("ABI stream did not close")
	}
	mu.Lock()
	text := output.String()
	mu.Unlock()
	return text, closeError, int(reads.Load())
}

func chatStreamFrame(t *testing.T, content, reasoning, finish string) []byte {
	t.Helper()
	delta := map[string]any{}
	if content != "" {
		delta["content"] = content
	}
	if reasoning != "" {
		delta["reasoning_content"] = reasoning
	}
	choice := map[string]any{"index": 0, "delta": delta}
	if finish != "" {
		choice["finish_reason"] = finish
	}
	body := marshalTest(t, map[string]any{
		"id": "chatcmpl-regression", "model": "test-model", "choices": []any{choice},
	})
	return []byte("data: " + string(body) + "\n\n")
}

func TestABIStreamPreservesFinishingDeltaAndTrailingUsage(t *testing.T) {
	for _, item := range []struct {
		name, content, expected string
	}{
		{"prose", "last fragment", "last fragment"},
		{"tool", nativeWeatherText + "\n", `\"city\":\"Tokyo\"`},
	} {
		t.Run(item.name, func(t *testing.T) {
			frames := append([]byte{}, chatStreamFrame(t, item.content, "last thought", "stop")...)
			frames = append(frames, chatStreamFrame(t, "must not appear", "", "")...)
			frames = append(frames, []byte("data: {\"choices\":[],\"usage\":{\"prompt_tokens\":12,\"completion_tokens\":7,\"total_tokens\":19,\"prompt_tokens_details\":{\"cached_tokens\":4}}}\n\ndata: [DONE]\n\n")...)
			body := []byte(strings.Replace(chatWeatherRequest, `"required"`, `"auto"`, 1))
			output, closeError, _ := runABIStream(t, body, "", frames)
			if closeError != "" {
				t.Fatalf("finishing delta failed: %s; output=%s", closeError, output)
			}
			for _, expected := range []string{item.expected, "last thought", `"cached_tokens":4`, "data: [DONE]"} {
				if !strings.Contains(output, expected) {
					t.Errorf("finishing delta or trailing usage lost %q: %s", expected, output)
				}
			}
			if strings.Contains(output, "must not appear") || strings.Contains(output, "<CPA_TC") {
				t.Errorf("post-finish content or raw tool tags leaked: %s", output)
			}
		})
	}
}

func TestABIStreamStopsReadingAfterToolChoiceViolation(t *testing.T) {
	for _, mode := range []string{"none", "named", "parallel"} {
		t.Run(mode, func(t *testing.T) {
			doc := ojson.NewDocString(chatWeatherRequest)
			text := nativeWeatherText + "\n"
			switch mode {
			case "none":
				if err := doc.Set("tool_choice", "none"); err != nil {
					t.Fatal(err)
				}
			case "named":
				if err := doc.Set("tools.1", map[string]any{"type": "function", "function": map[string]any{"name": "get_time", "parameters": map[string]any{"type": "object"}}}); err != nil {
					t.Fatal(err)
				}
				if err := doc.Set("tool_choice", map[string]any{"type": "function", "function": map[string]any{"name": "get_time"}}); err != nil {
					t.Fatal(err)
				}
			case "parallel":
				if err := doc.Set("parallel_tool_calls", false); err != nil {
					t.Fatal(err)
				}
				text += strings.Replace(nativeWeatherText, "Tokyo", "Osaka", 1) + "\n"
			}
			output, closeError, reads := runABIStream(t, doc.Bytes(), "", chatStreamFrame(t, text, "", ""), []byte("data: [DONE]\n\n"))
			if reads != 1 {
				t.Errorf("terminal violation consumed %d upstream callbacks, want 1", reads)
			}
			if closeError == "" || !strings.Contains(output, "tool_choice_violation") {
				t.Errorf("missing terminal choice error: close=%q; output=%s", closeError, output)
			}
			if strings.Count(output, "data: [DONE]") != 1 {
				t.Errorf("terminal frame was duplicated or omitted: %s", output)
			}
		})
	}
}

func TestABIStreamBufferLimitStopsBeforeFurtherReads(t *testing.T) {
	argument := strings.Replace(nativeWeatherText, "Tokyo", strings.Repeat("x", 600), 1)
	argument = strings.Replace(argument, "</CPA_TC|SEED1234>", "<CPA_TA|note|SEED1234>\n"+strings.Repeat("x", 600)+"\n</CPA_TA|SEED1234>\n</CPA_TC|SEED1234>", 1) + "\n"
	for _, item := range []struct {
		name, text string
		limited    bool
	}{
		{"aggregate-arguments", argument, true},
		{"incomplete-header", "<CPA_TC|get_weather|" + strings.Repeat("x", 1500), true},
		{"ordinary-prose", strings.Repeat("ordinary prose ", 300), false},
	} {
		t.Run(item.name, func(t *testing.T) {
			doc := ojson.NewDocString(chatWeatherRequest)
			if err := doc.Set("tool_choice", "auto"); err != nil {
				t.Fatal(err)
			}
			if err := doc.Set("tools.0.function.parameters.properties.note", map[string]any{"type": "string"}); err != nil {
				t.Fatal(err)
			}
			output, closeError, reads := runABIStream(t, doc.Bytes(), "max_stream_buffer_bytes: 1024\n", chatStreamFrame(t, item.text, "", ""), []byte("data: [DONE]\n\n"))
			if item.limited {
				if reads != 1 || !strings.Contains(closeError, "buffer limit") {
					t.Errorf("limit did not stop upstream: reads=%d, close=%q", reads, closeError)
				}
				if strings.Contains(output, `"finish_reason":"tool_calls"`) || strings.Contains(output, strings.Repeat("x", 100)) {
					t.Error("oversized arguments or a successful tool completion leaked")
				}
			} else if closeError != "" || !strings.Contains(output, "ordinary prose") {
				t.Errorf("buffer limit rejected ordinary streamed prose: close=%q", closeError)
			}
		})
	}
}

func TestABIStreamStopsAfterTerminalProtocolDiagnostic(t *testing.T) {
	body := []byte(strings.Replace(chatWeatherRequest, `"required"`, `"auto"`, 1))
	output, closeError, reads := runABIStream(t, body, "", chatStreamFrame(t, "<CPA_TC|get_weather|WRONG000>\n", "", ""), []byte("data: [DONE]\n\n"))
	if reads != 1 {
		t.Errorf("terminal protocol diagnostic consumed %d upstream callbacks, want 1", reads)
	}
	if closeError != "" || !strings.Contains(output, "Tool emulation stream parse error") || strings.Contains(output, "<CPA_TC") || strings.Count(output, "data: [DONE]") != 1 {
		t.Errorf("soft protocol failure did not complete cleanly: close=%q; output=%s", closeError, output)
	}
}
