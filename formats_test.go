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

var formatWeatherRequests = map[string]string{
	"openai":          chatWeatherRequest,
	"openai-response": `{"model":"test-model","input":"weather?","tools":[{"type":"function","name":"get_weather","parameters":{"type":"object","properties":{"city":{"type":"string"}}}}],"tool_choice":"required"}`,
	"claude":          `{"model":"test-model","max_tokens":500,"messages":[{"role":"user","content":[{"type":"text","text":"weather?"}]}],"tools":[{"name":"get_weather","input_schema":{"type":"object","properties":{"city":{"type":"string"}}}}],"tool_choice":{"type":"any"}}`,
	"gemini":          `{"model":"test-model","systemInstruction":{"parts":[{"text":"system"}]},"contents":[{"role":"user","parts":[{"text":"weather?"}]}],"tools":[{"functionDeclarations":[{"name":"get_weather","parameters":{"type":"object","properties":{"city":{"type":"string"}}}}]}],"toolConfig":{"functionCallingConfig":{"mode":"ANY"}}}`,
}

func TestABIClientFormatsUseCanonicalResponsesAndNativeRetrySchemas(t *testing.T) {
	for format, body := range formatWeatherRequests {
		t.Run(format, func(t *testing.T) {
			var requests []pluginapi.HostModelExecutionRequest
			p := NewPlugin(func(method string, raw []byte) ([]byte, error) {
				if method != pluginabi.MethodHostModelExecute {
					return nil, fmt.Errorf("unexpected method %s", method)
				}
				var request pluginapi.HostModelExecutionRequest
				if errDecode := json.Unmarshal(raw, &request); errDecode != nil {
					return nil, errDecode
				}
				requests = append(requests, request)
				text := nativeWeatherText
				if len(requests) == 1 {
					text = "<CPA_TC|get_weather|SEED1234>\n<CPA_TA|city|SEED1234>\nTokyo"
				}
				return json.Marshal(map[string]any{"ok": true, "result": pluginapi.HostModelExecutionResponse{StatusCode: 200, Body: nativeChatBody(text)}})
			})
			registerTest(t, p, "")
			result := callTest(t, p, pluginabi.MethodExecutorExecute, map[string]any{"SourceFormat": format, "Model": "test-model", "OriginalRequest": []byte(body), "host_callback_id": "lease"})
			var response pluginapi.ExecutorResponse
			if errDecode := json.Unmarshal(result, &response); errDecode != nil {
				t.Fatal(errDecode)
			}
			if len(requests) != 2 {
				t.Fatalf("retry count=%d", len(requests))
			}
			if ojson.NewDoc(response.Payload).Get("choices.0.message.tool_calls.0.function.name").String() != "get_weather" {
				t.Fatalf("not canonical tool output: %s", response.Payload)
			}
			for _, request := range requests {
				if request.EntryProtocol != format || request.ExitProtocol != "openai" {
					t.Fatalf("protocol mismatch: %s -> %s", request.EntryProtocol, request.ExitProtocol)
				}
				if ojson.NewDoc(request.Body).Get("tools").Exists() {
					t.Fatal("native tools survived folding")
				}
			}
			array := "messages"
			if format == "openai-response" {
				array = "input"
			}
			if format == "gemini" {
				array = "contents"
			}
			if len(ojson.NewDoc(requests[1].Body).Get(array).Array()) <= len(ojson.NewDoc(requests[0].Body).Get(array).Array()) {
				t.Fatal("retry was not appended in the client schema")
			}
			if format == "gemini" && strings.Contains(ojson.NewDoc(requests[0].Body).Get("systemInstruction.parts.0.text").String(), "<tools_doc>") {
				t.Fatal("Gemini tool prefix was left in unstable system metadata")
			}
		})
	}
}

func TestABIRejectsUndeclaredToolNames(t *testing.T) {
	p := NewPlugin(func(_ string, _ []byte) ([]byte, error) {
		return json.Marshal(map[string]any{"ok": true, "result": pluginapi.HostModelExecutionResponse{StatusCode: 200, Body: nativeChatBody(strings.Replace(nativeWeatherText, "get_weather", "not_declared", 1))}})
	})
	registerTest(t, p, "parse_retry: 0\n")
	doc := ojson.NewDoc([]byte(chatWeatherRequest))
	_ = doc.Set("tool_choice", "auto")
	raw, errHandle := p.Handle(pluginabi.MethodExecutorExecute, marshalTest(t, map[string]any{"SourceFormat": "openai", "Model": "test-model", "OriginalRequest": doc.Bytes(), "host_callback_id": "lease"}))
	if errHandle != nil {
		t.Fatal(errHandle)
	}
	var envelope pluginabi.Envelope
	if errDecode := json.Unmarshal(raw, &envelope); errDecode != nil {
		t.Fatal(errDecode)
	}
	if envelope.OK {
		t.Fatalf("undeclared tool was allowed: %s", envelope.Result)
	}
}
