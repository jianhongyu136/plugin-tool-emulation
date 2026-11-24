package main

import (
	"embed"
	"net/http"
	"strings"

	"github.com/MoeForge/moego/json"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

//go:embed web/config.html web/config.css web/config.js
var configPageAssets embed.FS

// registerManagement exposes only static resources; configuration uses the host's v8 interface.
func (p *Plugin) registerManagement(raw []byte) ([]byte, error) {
	var request pluginapi.ManagementRegistrationRequest
	if errDecode := json.Unmarshal(raw, &request); errDecode != nil {
		return failure("invalid_request", "invalid resource registration request", http.StatusBadRequest)
	}
	base := strings.TrimRight(request.ResourceBasePath, "/")
	if base == "" || !strings.HasPrefix(base, "/") || strings.ContainsAny(base, "?#") {
		return failure("invalid_request", "invalid plugin resource prefix", http.StatusBadRequest)
	}
	p.webBase.Store(&base)
	return success(pluginapi.ManagementRegistrationResponse{Resources: []pluginapi.ResourceRoute{
		{Path: "/config", Menu: "工具模拟配置", Description: "模型匹配规则、解析策略、前缀缓存及协议高级设置。"},
		{Path: "/assets/config.css"},
		{Path: "/assets/config.js"},
	}})
}

func (p *Plugin) serveManagementResource(raw []byte) ([]byte, error) {
	var request pluginapi.ManagementRequest
	if errDecode := json.Unmarshal(raw, &request); errDecode != nil {
		return failure("invalid_request", "invalid plugin resource request", http.StatusBadRequest)
	}
	base := p.webBase.Load()
	response := pluginapi.ManagementResponse{StatusCode: http.StatusNotFound, Headers: make(http.Header), Body: []byte("resource not found")}
	response.Headers.Set("X-Content-Type-Options", "nosniff")
	response.Headers.Set("Cache-Control", "no-store")
	response.Headers.Set("Referrer-Policy", "no-referrer")
	response.Headers.Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self' data:; base-uri 'none'; form-action 'none'; frame-ancestors 'self'")
	if base == nil {
		return success(response)
	}
	if request.Method != http.MethodGet {
		response.StatusCode = http.StatusMethodNotAllowed
		response.Headers.Set("Allow", http.MethodGet)
		return success(response)
	}
	var asset, contentType string
	switch request.Path {
	case *base + "/config":
		asset, contentType = "web/config.html", "text/html; charset=utf-8"
	case *base + "/assets/config.css":
		asset, contentType = "web/config.css", "text/css; charset=utf-8"
	case *base + "/assets/config.js":
		asset, contentType = "web/config.js", "text/javascript; charset=utf-8"
	default:
		return success(response)
	}
	body, errRead := configPageAssets.ReadFile(asset)
	if errRead != nil {
		return failure("resource_error", "embedded configuration resource is unavailable", http.StatusInternalServerError)
	}
	response.StatusCode, response.Body = http.StatusOK, body
	response.Headers.Set("Content-Type", contentType)
	return success(response)
}
