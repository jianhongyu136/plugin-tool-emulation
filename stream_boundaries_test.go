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

func TestABIStreamBoundaryMatrix(t *testing.T) {
	for _, test := range []struct {
		name, text, choice  string
		parallel, wantError bool
	}{
		{"tool", nativeWeatherText, "required", true, false},
		{"unicode", strings.ReplaceAll(nativeWeatherText, "Tokyo", "東京 🗼"), "required", true, false},
		{"plain", "No tool needed.", "auto", true, false},
		{"none", "No tool needed.", "none", true, false},
		{"required-prose", "No tool needed.", "required", true, true},
		{"none-tool", nativeWeatherText, "none", true, true},
		{"unknown-tool", strings.ReplaceAll(nativeWeatherText, "get_weather", "not_declared"), "auto", true, true},
		{"multiple-tools", nativeWeatherText + "\n" + nativeWeatherText, "auto", true, false},
		{"serial-multiple", nativeWeatherText + "\n" + nativeWeatherText, "auto", false, true},
		{"truncated-tool", "<CPA_TC|get_weather|SEED1234>\n<CPA_TA|city|SEED1234>\nTokyo", "required", true, true},
	} {
		for _, width := range []int{1, 7, 64, 4096} {
			t.Run(fmt.Sprintf("%s/bytes-%d", test.name, width), func(t *testing.T) {
				frame := marshalTest(t, map[string]any{"id": "boundary-stream", "choices": []any{map[string]any{"index": 0, "delta": map[string]any{"content": test.text}}}})
				sse := "data: " + string(frame) + "\n\ndata: {\"id\":\"boundary-stream\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"
				position := 0
				var mu sync.Mutex
				var output strings.Builder
				var upstreamCloses atomic.Int32
				closed := make(chan string, 1)
				p := NewPlugin(func(method string, raw []byte) ([]byte, error) {
					var result any = struct{}{}
					switch method {
					case pluginabi.MethodHostModelExecuteStream:
						result = pluginapi.HostModelStreamResponse{StreamID: "upstream-boundary"}
					case pluginabi.MethodHostModelStreamRead:
						end := position + width
						if end > len(sse) {
							end = len(sse)
						}
						result = pluginapi.HostModelStreamReadResponse{Payload: []byte(sse[position:end]), Done: end == len(sse)}
						position = end
					case pluginabi.MethodHostModelStreamClose:
						upstreamCloses.Add(1)
					case pluginabi.MethodHostStreamEmit:
						var event struct {
							Payload []byte `json:"payload"`
						}
						if errDecode := json.Unmarshal(raw, &event); errDecode != nil {
							return nil, errDecode
						}
						mu.Lock()
						output.Write(event.Payload)
						mu.Unlock()
					case pluginabi.MethodHostStreamClose:
						closed <- ojson.NewDoc(raw).Get("error").String()
					default:
						return nil, fmt.Errorf("unexpected host method %s", method)
					}
					return json.Marshal(map[string]any{"ok": true, "result": result})
				})
				registerTest(t, p, "")
				doc := ojson.NewDocString(chatWeatherRequest)
				if errSet := doc.Set("tool_choice", test.choice); errSet != nil {
					t.Fatal(errSet)
				}
				if errSet := doc.Set("parallel_tool_calls", test.parallel); errSet != nil {
					t.Fatal(errSet)
				}
				callTest(t, p, pluginabi.MethodExecutorExecuteStream, map[string]any{"SourceFormat": "openai", "Model": "test-model", "OriginalRequest": doc.Bytes(), "host_callback_id": "lease", "stream_id": "client-boundary"})
				select {
				case text := <-closed:
					if (text != "") != test.wantError {
						t.Fatalf("stream error=%q, wantError=%t", text, test.wantError)
					}
				case <-time.After(5 * time.Second):
					t.Fatal("stream did not terminate")
				}
				if upstreamCloses.Load() != 1 {
					t.Fatalf("upstream closes=%d", upstreamCloses.Load())
				}
				mu.Lock()
				answer := output.String()
				mu.Unlock()
				if !test.wantError && !strings.Contains(answer, "data: [DONE]") {
					t.Fatalf("missing final event: %s", answer)
				}
				if test.wantError && !strings.Contains(answer, `"error"`) {
					t.Fatalf("stream failure has no error event: %s", answer)
				}
				if strings.Contains(answer, "<CPA_TC") {
					t.Fatalf("raw protocol leaked: %s", answer)
				}
				if !test.wantError && strings.Contains(test.text, "<CPA_TC|") {
					arguments := map[int]string{}
					ids := map[string]bool{}
					for _, line := range strings.Split(answer, "\n") {
						if !strings.HasPrefix(line, "data: {") {
							continue
						}
						ojson.NewDocString(strings.TrimPrefix(line, "data: ")).Get("choices.0.delta.tool_calls").ForEach(func(_, call ojson.Result) bool {
							index := int(call.Get("index").Int())
							arguments[index] += call.Get("function.arguments").String()
							if id := call.Get("id").String(); id != "" {
								if ids[id] {
									t.Error("reused tool call ID")
								}
								ids[id] = true
							}
							return true
						})
					}
					wantCalls, wantCity := 1, "Tokyo"
					if test.name == "multiple-tools" {
						wantCalls = 2
					}
					if test.name == "unicode" {
						wantCity = "東京 🗼"
					}
					if len(arguments) != wantCalls || len(ids) != wantCalls {
						t.Fatalf("incomplete call boundaries: %s", answer)
					}
					for _, argument := range arguments {
						if ojson.NewDocString(argument).Get("city").String() != wantCity {
							t.Fatalf("corrupted fragmented arguments: %q", argument)
						}
					}
				}
			})
		}
	}
}

func TestABIQuiesceCancelsBlockedStreamAndClosesNativeHandle(t *testing.T) {
	readStarted, readReleased, clientClosed := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var readOnce, releaseOnce sync.Once
	var upstreamCloses atomic.Int32
	p := NewPlugin(func(method string, raw []byte) ([]byte, error) {
		var result any = struct{}{}
		switch method {
		case pluginabi.MethodHostModelExecuteStream:
			result = pluginapi.HostModelStreamResponse{StreamID: "blocked-upstream"}
		case pluginabi.MethodHostModelStreamRead:
			readOnce.Do(func() { close(readStarted) })
			<-readReleased
			result = pluginapi.HostModelStreamReadResponse{Done: true}
		case pluginabi.MethodHostModelStreamClose:
			upstreamCloses.Add(1)
			releaseOnce.Do(func() { close(readReleased) })
		case pluginabi.MethodHostStreamEmit:
		case pluginabi.MethodHostStreamClose:
			close(clientClosed)
		default:
			return nil, fmt.Errorf("unexpected host method %s", method)
		}
		return json.Marshal(map[string]any{"ok": true, "result": result})
	})
	t.Cleanup(func() { releaseOnce.Do(func() { close(readReleased) }) })
	registerTest(t, p, "")
	callTest(t, p, pluginabi.MethodExecutorExecuteStream, map[string]any{"SourceFormat": "openai", "Model": "test-model", "OriginalRequest": []byte(chatWeatherRequest), "host_callback_id": "lease", "stream_id": "blocked-client"})
	select {
	case <-readStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("native stream did not begin reading")
	}
	done := make(chan error, 1)
	go func() { _, errQuiesce := p.Handle(pluginabi.MethodPluginQuiesce, []byte(`{}`)); done <- errQuiesce }()
	select {
	case errQuiesce := <-done:
		if errQuiesce != nil {
			t.Fatal(errQuiesce)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("quiesce did not cancel blocked stream")
	}
	if upstreamCloses.Load() != 1 {
		t.Fatalf("native stream closed %d times", upstreamCloses.Load())
	}
	select {
	case <-clientClosed:
	default:
		t.Fatal("client stream not closed before quiesce completed")
	}
}
