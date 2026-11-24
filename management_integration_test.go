//go:build integration

package main

import (
	"bytes"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/MoeForge/moego/ojson"
)

func TestMainServesPluginConfigPageAndUsesAuthenticatedV8Configuration(t *testing.T) {
	fixture := startMainFixture(t)
	request := func(method, path, key string, body []byte) (int, http.Header, []byte) {
		t.Helper()
		req, errNew := http.NewRequest(method, fixture.url+path, bytes.NewReader(body))
		if errNew != nil {
			t.Fatal(errNew)
		}
		if key != "" {
			req.Header.Set("Authorization", "Bearer "+key)
		}
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		response, errSend := http.DefaultClient.Do(req)
		if errSend != nil {
			t.Fatal(errSend)
		}
		defer func() { _ = response.Body.Close() }()
		payload, errRead := io.ReadAll(response.Body)
		if errRead != nil {
			t.Fatal(errRead)
		}
		return response.StatusCode, response.Header, payload
	}
	for _, resource := range []struct{ path, contentType string }{{"/config", "text/html"}, {"/assets/config.css", "text/css"}, {"/assets/config.js", "text/javascript"}} {
		status, headers, body := request("GET", "/v0/resource/plugins/tool-emulation"+resource.path, "", nil)
		if status != 200 || !strings.HasPrefix(headers.Get("Content-Type"), resource.contentType) {
			t.Fatalf("resource %s: HTTP %d: %s\n%s", resource.path, status, body, fixture.log.String())
		}
		if bytes.Contains(body, []byte("page-"+"management")) || bytes.Contains(body, []byte("integration-upstream")) {
			t.Fatal("public page disclosed credentials")
		}
	}
	configPath := "/v8/management/config/plugins/configs/tool-emulation"
	if status, _, _ := request("GET", configPath, "", nil); status != 401 {
		t.Fatalf("unauthenticated config read: HTTP %d", status)
	}
	if status, _, _ := request("PUT", configPath, "", []byte(`{"enabled":false}`)); status != 401 {
		t.Fatalf("unauthenticated config write: HTTP %d", status)
	}
	key := "page-" + "management"
	status, _, list := request("GET", "/v8/management/plugins", key, nil)
	if status != 200 || !bytes.Contains(list, []byte("/v0/resource/plugins/tool-emulation/config")) {
		t.Fatalf("custom page menu missing: %s", list)
	}
	status, headers, config := request("GET", configPath, key, nil)
	if status != 200 {
		t.Fatalf("read config: HTTP %d: %s", status, config)
	}
	if headers.Get("ETag") == "" {
		t.Fatal("config read has no concurrency token")
	}
	doc := ojson.NewDoc(config)
	if errSet := doc.Set("parse_retry", 2); errSet != nil {
		t.Fatal(errSet)
	}
	if errSet := doc.Set("cache_ttl", "1h"); errSet != nil {
		t.Fatal(errSet)
	}
	if errSet := doc.Set("priority", 33); errSet != nil {
		t.Fatal(errSet)
	}
	if errSet := doc.Set("custom_preserved", "unchanged"); errSet != nil {
		t.Fatal(errSet)
	}
	status, _, answer := request("PUT", configPath, key, doc.Bytes())
	if status != 200 {
		t.Fatalf("save config: HTTP %d: %s", status, answer)
	}
	status, _, saved := request("GET", configPath, key, nil)
	if status != 200 || ojson.NewDoc(saved).Get("parse_retry").Int() != 2 || ojson.NewDoc(saved).Get("custom_preserved").String() != "unchanged" || !ojson.NewDoc(saved).Get("rules.0.models").Exists() {
		t.Fatalf("config was not persisted without dropping unrelated fields: %s", saved)
	}
	status, _, body := request("GET", "/v0/resource/plugins/tool-emulation/config", "", nil)
	if status != 200 || bytes.Contains(body, []byte("custom_preserved")) {
		t.Fatal("page exposed live configuration after saving")
	}
}
