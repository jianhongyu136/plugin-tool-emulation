package main

import (
	"strings"
	"testing"

	"github.com/MoeForge/moego/json"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func TestABIRejectsAmbiguousTagsWithoutReplacingConfiguration(t *testing.T) {
	for _, tags := range []string{
		"tool: TAG\n  arg: TAG\n  result: RESULT",
		"tool: TAG\n  arg: ARG\n  result: TAG",
		"tool: TOOL\n  arg: TAG\n  result: TAG",
		"tool: CPA_TR",
		"arg: CPA_TC",
	} {
		t.Run(strings.ReplaceAll(tags, "\n", "/"), func(t *testing.T) {
			p := NewPlugin(nil)
			registerTest(t, p, "")
			raw, err := p.Handle(pluginabi.MethodPluginReconfigure, marshalTest(t, map[string]any{
				"schema_version": pluginabi.SchemaVersion,
				"config_yaml":    []byte("enabled: false\ntag_group:\n  " + tags + "\n"),
			}))
			if err != nil {
				t.Fatal(err)
			}
			var envelope pluginabi.Envelope
			if err := json.Unmarshal(raw, &envelope); err != nil {
				t.Fatal(err)
			}
			if envelope.OK {
				t.Error("ambiguous protocol tags were accepted")
			}
			result := callTest(t, p, pluginabi.MethodModelRoute, pluginapi.ModelRouteRequest{SourceFormat: "openai", RequestedModel: "test-model", Body: []byte(chatWeatherRequest)})
			var route pluginapi.ModelRouteResponse
			if err := json.Unmarshal(result, &route); err != nil {
				t.Fatal(err)
			}
			if !route.Handled {
				t.Error("rejected reconfiguration replaced the active routing snapshot")
			}
		})
	}
}
