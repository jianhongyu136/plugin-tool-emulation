package main

import (
	"testing"

	"github.com/MoeForge/moego/json"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func TestABIRoutingBoundaries(t *testing.T) {
	for _, test := range []struct {
		name, config, model, format, body string
		handled                           bool
	}{
		{"selected", "rules:\n  - models: [chosen-*]\n", "chosen-model", "openai", chatWeatherRequest, true},
		{"unselected", "rules:\n  - models: [chosen-*]\n", "other-model", "openai", chatWeatherRequest, false},
		{"alias-selected", "rules:\n  - model_aliases: ['chosen-?']\n", "chosen-a", "openai", chatWeatherRequest, true},
		{"alias-whole-pattern", "rules:\n  - model_aliases: ['chosen-?']\n", "chosen-abc", "openai", chatWeatherRequest, false},
		{"disabled", "enabled: false\nrules:\n  - models: ['*']\n", "test-model", "openai", chatWeatherRequest, false},
		{"explicit-wildcard", "rules:\n  - models: ['*']\n", "test-model", "openai", chatWeatherRequest, true},
		{"unsupported-format", "rules:\n  - models: ['*']\n", "test-model", "unknown", chatWeatherRequest, false},
		{"no-tools", "rules:\n  - models: ['*']\n", "test-model", "openai", `{"messages":[{"role":"user","content":"hello"}]}`, false},
		{"empty-tools", "rules:\n  - models: ['*']\n", "test-model", "openai", `{"messages":[{"role":"user","content":"hello"}],"tools":[]}`, false},
		{"null-tools", "rules:\n  - models: ['*']\n", "test-model", "openai", `{"messages":[],"tools":null}`, false},
		{"history-only", "rules:\n  - models: ['*']\n", "test-model", "openai", `{"messages":[{"role":"tool","tool_call_id":"history-id","content":"prior result"}]}`, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			p := NewPlugin(nil)
			config := test.config
			if test.name != "disabled" {
				config = "enabled: true\n" + config
			}
			callTest(t, p, pluginabi.MethodPluginRegister, map[string]any{"schema_version": pluginabi.SchemaVersion, "config_yaml": []byte(config)})
			result := callTest(t, p, pluginabi.MethodModelRoute, pluginapi.ModelRouteRequest{RequestedModel: test.model, SourceFormat: test.format, Body: []byte(test.body)})
			var route pluginapi.ModelRouteResponse
			if errDecode := json.Unmarshal(result, &route); errDecode != nil {
				t.Fatal(errDecode)
			}
			if route.Handled != test.handled {
				t.Fatalf("handled=%t, want %t", route.Handled, test.handled)
			}
		})
	}
}
