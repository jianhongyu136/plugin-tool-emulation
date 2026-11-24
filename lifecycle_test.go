package main

import (
	"testing"

	"github.com/MoeForge/moego/json"
	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func TestABICacheNormalizerIsIdempotentAndProtectsPrefixBudget(t *testing.T) {
	p := NewPlugin(nil)
	registerTest(t, p, "")
	for _, ttl := range []string{"5m", "1h"} {
		t.Run(ttl, func(t *testing.T) {
			marker := map[string]any{"type": "ephemeral"}
			blocks := []any{map[string]any{"type": "text", "text": cachePragma + ttl + " -->\n<tools_doc>\n{}\n</tools_doc>\n<tool_protocol>\nfixed\n</tool_protocol>"}}
			for range 5 {
				blocks = append(blocks, map[string]any{"type": "text", "text": "caller", "cache_control": marker})
			}
			body := marshalTest(t, map[string]any{"system": []any{map[string]any{"type": "text", "text": "system", "cache_control": marker}}, "messages": []any{map[string]any{"role": "user", "content": blocks}}, "thread": "client-thread"})
			first := callTest(t, p, pluginabi.MethodRequestNormalize, pluginapi.RequestTransformRequest{ToFormat: "claude", Body: body})
			var transformed struct{ Body []byte }
			if errDecode := json.Unmarshal(first, &transformed); errDecode != nil {
				t.Fatal(errDecode)
			}
			doc := ojson.NewDoc(transformed.Body)
			if doc.Get("messages.0.content.0.cache_control.ttl").String() != ttl || doc.Get("prompt_cache_options.mode").String() != "explicit" {
				t.Fatalf("missing cache protection: %s", transformed.Body)
			}
			count := 0
			doc.Get("messages").ForEach(func(_, message ojson.Result) bool {
				message.Get("content").ForEach(func(_, part ojson.Result) bool {
					if part.Get("cache_control").Exists() {
						count++
					}
					return true
				})
				return true
			})
			if count != 2 || doc.Get("system.0.cache_control").Exists() || doc.Get("thread").String() != "client-thread" {
				t.Fatalf("invalid cache budget: %s", transformed.Body)
			}
			second := callTest(t, p, pluginabi.MethodRequestNormalize, pluginapi.RequestTransformRequest{ToFormat: "claude", Body: transformed.Body})
			var repeated struct{ Body []byte }
			if errDecode := json.Unmarshal(second, &repeated); errDecode != nil {
				t.Fatal(errDecode)
			}
			if string(repeated.Body) != string(transformed.Body) {
				t.Fatal("cache normalization is not idempotent")
			}
		})
	}
}

func TestABIInvalidReconfigureLeavesPreviousRulesActive(t *testing.T) {
	p := NewPlugin(nil)
	registerTest(t, p, "")
	raw, errHandle := p.Handle(pluginabi.MethodPluginReconfigure, marshalTest(t, map[string]any{"schema_version": pluginabi.SchemaVersion, "config_yaml": []byte("enabled: true\nparse_retry: -1\n")}))
	if errHandle != nil {
		t.Fatal(errHandle)
	}
	var envelope pluginabi.Envelope
	if errDecode := json.Unmarshal(raw, &envelope); errDecode != nil {
		t.Fatal(errDecode)
	}
	if envelope.OK {
		t.Fatal("invalid config was accepted")
	}
	result := callTest(t, p, pluginabi.MethodModelRoute, pluginapi.ModelRouteRequest{RequestedModel: "test-model", SourceFormat: "openai", Body: []byte(chatWeatherRequest)})
	var route pluginapi.ModelRouteResponse
	if errDecode := json.Unmarshal(result, &route); errDecode != nil {
		t.Fatal(errDecode)
	}
	if !route.Handled || route.TargetKind != pluginapi.ModelRouteTargetSelf {
		t.Fatal("previous configuration was lost")
	}
}
