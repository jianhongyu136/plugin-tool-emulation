package main

import (
	"testing"

	"github.com/MoeForge/moego/json"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func TestABIEmptyModelRulesDoNotInterceptRequests(t *testing.T) {
	for _, config := range []string{"enabled: true\n", "enabled: true\nrules: []\n", "enabled: true\nrules:\n  - {}\n", "enabled: true\nrules:\n  - provider: claude\n", "enabled: true\nrules:\n  - models: [' ', '']\n    model_aliases: ['']\n"} {
		p := NewPlugin(nil)
		callTest(t, p, pluginabi.MethodPluginRegister, map[string]any{"schema_version": pluginabi.SchemaVersion, "config_yaml": []byte(config)})
		for _, model := range []string{"test-model", "another-model"} {
			result := callTest(t, p, pluginabi.MethodModelRoute, pluginapi.ModelRouteRequest{RequestedModel: model, SourceFormat: "openai", Body: []byte(chatWeatherRequest)})
			var route pluginapi.ModelRouteResponse
			if errDecode := json.Unmarshal(result, &route); errDecode != nil {
				t.Fatal(errDecode)
			}
			if route.Handled {
				t.Fatalf("enabled plugin with no chosen models intercepted %q, config=%q", model, config)
			}
		}
	}
}
