//go:build integration

package main

import (
	"bytes"
	"io"
	"net/http"
	"testing"

	"github.com/MoeForge/moego/ojson"
)

func TestMainEnabledWithoutModelRulesLeavesNativeCallsUntouched(t *testing.T) {
	fixture := startMainFixture(t, true)
	body := fixture.post(t, "/v1/chat/completions", []byte(chatWeatherRequest))
	if ojson.NewDoc(body).Get("choices.0.message.tool_calls").Exists() {
		t.Fatalf("empty model rules emulated a native response: %s", body)
	}
	upstream := <-fixture.captured
	if !ojson.NewDoc(upstream.body).Get("tools.0.function.name").Exists() || bytes.Contains(upstream.body, []byte("<tool_protocol>")) {
		t.Fatal("empty model rules changed the upstream request")
	}
}

func TestMainModelInventoryIsAvailableForTheConfigPicker(t *testing.T) {
	fixture := startMainFixture(t)
	get := func(path, key string) []byte {
		t.Helper()
		req, errNew := http.NewRequest("GET", fixture.url+path, nil)
		if errNew != nil {
			t.Fatal(errNew)
		}
		if key != "" {
			req.Header.Set("Authorization", "Bearer "+key)
		}
		response, errSend := http.DefaultClient.Do(req)
		if errSend != nil {
			t.Fatal(errSend)
		}
		defer func() { _ = response.Body.Close() }()
		body, errRead := io.ReadAll(response.Body)
		if errRead != nil {
			t.Fatal(errRead)
		}
		if response.StatusCode != 200 {
			t.Fatalf("%s: HTTP %d: %s", path, response.StatusCode, body)
		}
		return body
	}
	models := get("/v1/models", "integration-client")
	for _, id := range []string{"test-model", "filter-model", "claude-model"} {
		if !bytes.Contains(models, []byte(id)) {
			t.Fatalf("CPA catalog has no %s: %s", id, models)
		}
	}
	get("/v8/management/credentials", "page-"+"management")
	get("/v8/management/credentials/models?name=missing-credential", "page-"+"management")
	javascript := get("/v0/resource/plugins/tool-emulation/assets/config.js", "")
	if !bytes.Contains(javascript, []byte("/v1/models")) || !bytes.Contains(javascript, []byte("/v8/management/credentials/models")) {
		t.Fatal("custom config picker is not wired to the existing CPA model interfaces")
	}
}
