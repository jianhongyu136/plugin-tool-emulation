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

func TestABIGeminiPreservesAlternativeParameterSchema(t *testing.T) {
	const schema = `{"type":"object","properties":{"count":{"type":"integer"},"enabled":{"type":"boolean"},"filter":{"type":"object"},"items":{"type":"array"},"note":{"type":"string"}}}`
	const stringSchema = `{"type":"object","properties":{"count":{"type":"string"},"enabled":{"type":"string"},"filter":{"type":"string"},"items":{"type":"string"},"note":{"type":"string"}}}`
	const text = "<CPA_TC|get_weather|SEED1234>\n<CPA_TA|count|SEED1234>\n12\n</CPA_TA|SEED1234>\n<CPA_TA|enabled|SEED1234>\ntrue\n</CPA_TA|SEED1234>\n<CPA_TA|filter|SEED1234>\n{\"k\":\"v\"}\n</CPA_TA|SEED1234>\n<CPA_TA|items|SEED1234>\n[1,2]\n</CPA_TA|SEED1234>\n<CPA_TA|note|SEED1234>\n001\n</CPA_TA|SEED1234>\n</CPA_TC|SEED1234>"
	const expectedArgs = `{"count":12,"enabled":true,"filter":{"k":"v"},"items":[1,2],"note":"001"}`
	for _, field := range []string{"parameters", "parametersJsonSchema", "both"} {
		t.Run(field, func(t *testing.T) {
			doc := ojson.NewDocString(formatWeatherRequests["gemini"])
			if err := doc.Delete("tools.0.functionDeclarations.0.parameters"); err != nil {
				t.Fatal(err)
			}
			path := "tools.0.functionDeclarations.0." + field
			if field == "both" {
				path = "tools.0.functionDeclarations.0.parameters"
				if err := doc.SetRaw("tools.0.functionDeclarations.0.parametersJsonSchema", stringSchema); err != nil {
					t.Fatal(err)
				}
			}
			if err := doc.SetRaw(path, schema); err != nil {
				t.Fatal(err)
			}
			var sent pluginapi.HostModelExecutionRequest
			p := NewPlugin(func(method string, raw []byte) ([]byte, error) {
				if method != pluginabi.MethodHostModelExecute {
					return nil, fmt.Errorf("unexpected host callback %s", method)
				}
				if err := json.Unmarshal(raw, &sent); err != nil {
					return nil, err
				}
				return json.Marshal(map[string]any{"ok": true, "result": pluginapi.HostModelExecutionResponse{StatusCode: 200, Body: nativeChatBody(text)}})
			})
			registerTest(t, p, "")
			result := callTest(t, p, pluginabi.MethodExecutorExecute, map[string]any{
				"SourceFormat": "gemini", "Model": "test-model", "OriginalRequest": doc.Bytes(), "host_callback_id": "schema-lease",
			})
			var response pluginapi.ExecutorResponse
			if err := json.Unmarshal(result, &response); err != nil {
				t.Fatal(err)
			}
			args := ojson.NewDoc(response.Payload).Get("choices.0.message.tool_calls.0.function.arguments").String()
			if args != expectedArgs {
				t.Errorf("typed arguments lost for %s: got %s, want %s", field, args, expectedArgs)
			}
			if !strings.Contains(string(sent.Body), "integer") || ojson.NewDoc(sent.Body).Get("tools").Exists() {
				t.Errorf("folded prompt lost schema or retained native tools: %s", sent.Body)
			}
		})
	}
}

func TestABIGeminiHonorsAllowedFunctionNames(t *testing.T) {
	for _, cached := range []bool{false, true} {
		for _, name := range []string{"get_weather", "get_time", "excluded_tool"} {
			t.Run(fmt.Sprintf("cache-%t/%s", cached, name), func(t *testing.T) {
				doc := ojson.NewDocString(formatWeatherRequests["gemini"])
				declarations := make([]any, 0, 3)
				for _, toolName := range []string{"get_weather", "get_time", "excluded_tool"} {
					declarations = append(declarations, map[string]any{"name": toolName, "parameters": map[string]any{"type": "object", "properties": map[string]any{"city": map[string]any{"type": "string"}}}})
				}
				if err := doc.Set("tools.0.functionDeclarations", declarations); err != nil {
					t.Fatal(err)
				}
				if err := doc.Set("toolConfig.functionCallingConfig.allowedFunctionNames", []string{"get_weather", "get_time"}); err != nil {
					t.Fatal(err)
				}
				text := strings.Replace(nativeWeatherText, "get_weather", name, 1)
				config := fmt.Sprintf("prefix_cache: %t\nparse_retry: 0\n", cached)
				var sent pluginapi.HostModelExecutionRequest
				p := NewPlugin(func(method string, raw []byte) ([]byte, error) {
					if method != pluginabi.MethodHostModelExecute {
						return nil, fmt.Errorf("unexpected callback %s", method)
					}
					if err := json.Unmarshal(raw, &sent); err != nil {
						return nil, err
					}
					return json.Marshal(map[string]any{"ok": true, "result": pluginapi.HostModelExecutionResponse{StatusCode: 200, Body: nativeChatBody(text)}})
				})
				registerTest(t, p, config)
				raw, err := p.Handle(pluginabi.MethodExecutorExecute, marshalTest(t, map[string]any{"SourceFormat": "gemini", "Model": "test-model", "OriginalRequest": doc.Bytes(), "host_callback_id": "allowed-lease"}))
				if err != nil {
					t.Fatal(err)
				}
				var envelope pluginabi.Envelope
				if err := json.Unmarshal(raw, &envelope); err != nil {
					t.Fatal(err)
				}
				allowed := name != "excluded_tool"
				if envelope.OK != allowed {
					t.Errorf("allowed subset not enforced for %q: %s", name, raw)
				}
				var prompt strings.Builder
				ojson.NewDoc(sent.Body).Get("contents.0.parts").ForEach(func(_, part ojson.Result) bool { prompt.WriteString(part.Get("text").String()); return true })
				if !strings.Contains(prompt.String(), `Only these tool names are permitted for this response: ["get_time","get_weather"].`) {
					t.Error("allowed subset missing from effective upstream instructions")
				}
				output, closeError, reads := runABIStreamFormat(t, "gemini", doc.Bytes(), config, chatStreamFrame(t, text+"\n", "", ""), []byte("data: [DONE]\n\n"))
				if allowed {
					if closeError != "" || !strings.Contains(output, `"name":"`+name+`"`) {
						t.Errorf("permitted stream failed: close=%q; output=%s", closeError, output)
					}
				} else if closeError == "" || reads != 1 || strings.Contains(output, `"name":"excluded_tool"`) || !strings.Contains(output, "tool_choice_violation") {
					t.Errorf("excluded stream did not stop cleanly: reads=%d; close=%q; output=%s", reads, closeError, output)
				}
			})
		}
	}
}
