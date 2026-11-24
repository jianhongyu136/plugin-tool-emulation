package main

import (
	"bytes"
	"net/http"
	"strings"
	"testing"

	"github.com/MoeForge/moego/json"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func TestABIRegistersStandaloneConfigPageWithoutEmbeddingConfiguration(t *testing.T) {
	p := NewPlugin(nil)
	registerTest(t, p, "parse_retry: 8\n")
	metadata := callTest(t, p, pluginabi.MethodPluginRegister, map[string]any{"schema_version": pluginabi.SchemaVersion, "config_yaml": []byte("enabled: true\nparse_retry: 8\n")})
	var registration struct {
		Capabilities struct {
			ManagementAPI bool `json:"management_api"`
		} `json:"capabilities"`
	}
	if errDecode := json.Unmarshal(metadata, &registration); errDecode != nil {
		t.Fatal(errDecode)
	}
	if !registration.Capabilities.ManagementAPI {
		t.Fatal("plugin did not advertise its custom page")
	}

	result := callTest(t, p, pluginabi.MethodManagementRegister, pluginapi.ManagementRegistrationRequest{ResourceBasePath: "/v0/resource/plugins/tool-emulation", BasePath: "/v0/management"})
	var routes pluginapi.ManagementRegistrationResponse
	if errDecode := json.Unmarshal(result, &routes); errDecode != nil {
		t.Fatal(errDecode)
	}
	if len(routes.Routes) != 0 {
		t.Fatal("custom page added a deprecated management endpoint")
	}
	paths := make(map[string]bool)
	for _, resource := range routes.Resources {
		paths[resource.Path] = true
	}
	for _, path := range []string{"/config", "/assets/config.css", "/assets/config.js"} {
		if !paths[path] {
			t.Fatalf("missing resource %s", path)
		}
	}
	if routes.Resources[0].Menu == "" {
		t.Fatal("config page has no menu entry")
	}

	var response pluginapi.ManagementResponse
	body := callTest(t, p, pluginabi.MethodManagementHandle, pluginapi.ManagementRequest{Method: http.MethodGet, Path: "/v0/resource/plugins/tool-emulation/config"})
	if errDecode := json.Unmarshal(body, &response); errDecode != nil {
		t.Fatal(errDecode)
	}
	if response.StatusCode != http.StatusOK || !strings.HasPrefix(response.Headers.Get("Content-Type"), "text/html") {
		t.Fatalf("not an HTML page: %#v", response)
	}
	if !bytes.Contains(response.Body, []byte("工具模拟配置")) || !bytes.Contains(response.Body, []byte("assets/config.js")) {
		t.Fatal("page is not usable without another server")
	}
	if bytes.Contains(response.Body, []byte(`"parse_retry":8`)) || bytes.Contains(response.Body, []byte("integration-management")) {
		t.Fatal("public resource embedded live configuration or credentials")
	}
	if response.Headers.Get("Content-Security-Policy") == "" || response.Headers.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal("missing resource safety headers")
	}

	body = callTest(t, p, pluginabi.MethodManagementHandle, pluginapi.ManagementRequest{Method: http.MethodGet, Path: "/v0/resource/plugins/tool-emulation/assets/config.js"})
	if errDecode := json.Unmarshal(body, &response); errDecode != nil {
		t.Fatal(errDecode)
	}
	if !bytes.Contains(response.Body, []byte("/v8/management/config/plugins/configs/")) || bytes.Contains(response.Body, []byte("localStorage")) || bytes.Contains(response.Body, []byte("/v0/management")) {
		t.Fatal("page does not use the authenticated v8 configuration interface safely")
	}
	body = callTest(t, p, pluginabi.MethodManagementHandle, pluginapi.ManagementRequest{Method: http.MethodGet, Path: "/v0/resource/plugins/tool-emulation/not-registered"})
	if errDecode := json.Unmarshal(body, &response); errDecode != nil {
		t.Fatal(errDecode)
	}
	if response.StatusCode != http.StatusNotFound {
		t.Fatal("unknown resource was served")
	}
}
