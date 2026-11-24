//go:build integration

package main

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MoeForge/moego/ojson"
	"gopkg.in/yaml.v3"
)

type processLog struct {
	mu   sync.Mutex
	data bytes.Buffer
}

func (log *processLog) Write(data []byte) (int, error) {
	log.mu.Lock()
	defer log.mu.Unlock()
	return log.data.Write(data)
}
func (log *processLog) String() string {
	log.mu.Lock()
	defer log.mu.Unlock()
	return log.data.String()
}

type capturedRequest struct {
	path string
	body []byte
}
type mainFixture struct {
	url      string
	captured chan capturedRequest
	log      *processLog
}

type mainFixtureOptions struct {
	emptyModelRules bool
	privacyMode     string
	privacyPriority int
	privacyBuiltins bool
	replyText       func([]byte) string
}

func startMainFixture(t *testing.T, emptyModelRules ...bool) *mainFixture {
	options := mainFixtureOptions{}
	if len(emptyModelRules) > 0 {
		options.emptyModelRules = emptyModelRules[0]
	}
	return startMainFixtureWithOptions(t, options)
}

func startMainFixtureWithOptions(t *testing.T, options mainFixtureOptions) *mainFixture {
	t.Helper()
	root, errRoot := os.Getwd()
	if errRoot != nil {
		t.Fatal(errRoot)
	}
	executable, library := "cli-proxy-api", "tool-emulation-linux-amd64.so"
	if runtime.GOOS == "windows" {
		executable += ".exe"
		library = "tool-emulation-windows-amd64.dll"
	}
	host := filepath.Join(root, ".test-bin", executable)
	plugin := filepath.Join(root, "dist", library)
	for _, file := range []string{host, plugin} {
		if _, errStat := os.Stat(file); errStat != nil {
			t.Fatalf("build host/plugin first: %s: %v", file, errStat)
		}
	}
	captured := make(chan capturedRequest, 64)
	var retryMu sync.Mutex
	retrySeen := false
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, errRead := io.ReadAll(request.Body)
		if errRead != nil {
			http.Error(writer, errRead.Error(), 500)
			return
		}
		captured <- capturedRequest{request.URL.Path, body}
		doc := ojson.NewDoc(body)
		replyText := nativeWeatherText
		if options.replyText != nil {
			replyText = options.replyText(body)
		}
		if request.URL.Path == "/v1/messages" {
			if doc.Get("stream").Bool() {
				writer.Header().Set("Content-Type", "text/event-stream")
				start := map[string]any{"type": "message_start", "message": map[string]any{"id": "msg-native", "type": "message", "role": "assistant", "model": "claude-upstream", "content": []any{}, "usage": map[string]any{"input_tokens": 12, "output_tokens": 0, "cache_read_input_tokens": 4}}}
				_, _ = fmt.Fprintf(writer, "event: message_start\ndata: %s\n\n", marshalTest(t, start))
				_, _ = io.WriteString(writer, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n")
				delta := map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "text_delta", "text": replyText}}
				_, _ = fmt.Fprintf(writer, "event: content_block_delta\ndata: %s\n\n", marshalTest(t, delta))
				_, _ = io.WriteString(writer, "event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":7}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
				return
			}
			writer.Header().Set("Content-Type", "application/json")
			answer := map[string]any{"id": "msg-native", "type": "message", "role": "assistant", "model": "claude-upstream", "content": []any{map[string]any{"type": "text", "text": replyText}}, "stop_reason": "end_turn", "usage": map[string]any{"input_tokens": 12, "output_tokens": 7, "cache_read_input_tokens": 4}}
			_, _ = writer.Write(marshalTest(t, answer))
			return
		}
		if doc.Get("stream").Bool() {
			writer.Header().Set("Content-Type", "text/event-stream")
			for _, text := range []string{replyText[:6], replyText[6:]} {
				frame := map[string]any{"id": "chatcmpl-native", "model": "native-model", "choices": []any{map[string]any{"index": 0, "delta": map[string]any{"content": text}}}}
				_, _ = fmt.Fprintf(writer, "data: %s\n\n", marshalTest(t, frame))
			}
			_, _ = io.WriteString(writer, "data: {\"id\":\"chatcmpl-native\",\"model\":\"native-model\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		text := replyText
		if doc.Get("metadata.retry_test").Bool() {
			retryMu.Lock()
			if !retrySeen {
				text = "<CPA_TC|get_weather|SEED1234>\n<CPA_TA|city|SEED1234>\nTokyo"
				retrySeen = true
			}
			retryMu.Unlock()
		}
		_, _ = writer.Write(nativeChatBody(text))
	}))
	t.Cleanup(upstream.Close)
	directory := t.TempDir()
	pluginDir := filepath.Join(directory, "plugins")
	if errMake := os.MkdirAll(pluginDir, 0700); errMake != nil {
		t.Fatal(errMake)
	}
	binary, errRead := os.ReadFile(plugin)
	if errRead != nil {
		t.Fatal(errRead)
	}
	if errWrite := os.WriteFile(filepath.Join(pluginDir, "tool-emulation"+filepath.Ext(plugin)), binary, 0600); errWrite != nil {
		t.Fatal(errWrite)
	}
	listener, errListen := net.Listen("tcp", "127.0.0.1:0")
	if errListen != nil {
		t.Fatal(errListen)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if errClose := listener.Close(); errClose != nil {
		t.Fatal(errClose)
	}
	config := map[string]any{
		"config-version": 8,
		"server":         map[string]any{"host": "127.0.0.1", "port": port, "shutdown-timeout": 0, "response-compression": map[string]any{"enabled": false}},
		"management":     map[string]any{"disable-control-panel": true, "secret-" + "key": "page-" + "management"},
		"access":         map[string]any{"api-keys": []string{"integration-client"}},
		"oauth":          map[string]any{"auth-dir": filepath.Join(directory, "auths")},
		"routing":        map[string]any{"retry": map[string]any{"request-retry": 0}},
		"api-keys": map[string]any{
			"openai-compatibility": []any{map[string]any{"name": "test-provider", "base-url": upstream.URL + "/v1", "support-prompt-cache-key": true, "keys": []any{map[string]any{"api-key": "integration-upstream"}}, "models": []any{map[string]any{"name": "native-model", "alias": "test-model"}, map[string]any{"name": "filter-upstream", "alias": "filter-model"}}}},
			"claude":               []any{map[string]any{"name": "test-claude", "base-url": upstream.URL, "keys": []any{map[string]any{"api-key": "integration-claude", "cloak": map[string]any{"mode": "never"}}}, "models": []any{map[string]any{"name": "claude-upstream", "alias": "claude-model"}}}},
		},
		"plugins":  map[string]any{"enabled": true, "dir": pluginDir, "configs": map[string]any{"tool-emulation": map[string]any{"enabled": true, "fence_" + "token": "SEED1234", "rules": []any{map[string]any{"provider": "openai-compatible-test-provider", "models": []string{"test-model", "filter-model"}}, map[string]any{"provider": "claude", "models": []string{"claude-model"}}}}}},
		"requests": map[string]any{"payload": map[string]any{"filter": []any{map[string]any{"models": []any{map[string]any{"name": "filter-*", "protocol": "openai"}}, "params": []string{"messages.1"}}}}},
	}
	if options.emptyModelRules {
		pluginConfigs := config["plugins"].(map[string]any)["configs"].(map[string]any)
		pluginConfigs[pluginIdentifier].(map[string]any)["rules"] = []any{}
	}
	if options.privacyMode != "" {
		privacyLibrary := "privacy-filter-linux-amd64.so"
		if runtime.GOOS == "windows" {
			privacyLibrary = "privacy-filter-windows-amd64.dll"
		}
		privacySource := filepath.Join(root, "..", "plugin-privacy-filter", "dist", privacyLibrary)
		privacyBinary, errReadPrivacy := os.ReadFile(privacySource)
		if errReadPrivacy != nil {
			t.Fatalf("build privacy plugin first: %v", errReadPrivacy)
		}
		if errWritePrivacy := os.WriteFile(filepath.Join(pluginDir, "privacy-filter"+filepath.Ext(privacySource)), privacyBinary, 0600); errWritePrivacy != nil {
			t.Fatal(errWritePrivacy)
		}
		config["plugins"].(map[string]any)["configs"].(map[string]any)["privacy-filter"] = map[string]any{"enabled": true, "mode": options.privacyMode, "priority": options.privacyPriority, "token_label": "PRIVATE", "builtin_rules_enabled": options.privacyBuiltins, "custom_value_rules": []any{map[string]any{"name": "boundary-marker", "regex": "BOUNDARY-[A-Z0-9]{12}"}}}
	}
	yamlBody, errEncode := yaml.Marshal(config)
	if errEncode != nil {
		t.Fatal(errEncode)
	}
	configPath := filepath.Join(directory, "config.yaml")
	if errWrite := os.WriteFile(configPath, yamlBody, 0600); errWrite != nil {
		t.Fatal(errWrite)
	}
	log := &processLog{}
	command := exec.Command(host, "--config", configPath, "--local-model")
	command.Dir = directory
	command.Stdout, command.Stderr = log, log
	if errStart := command.Start(); errStart != nil {
		t.Fatal(errStart)
	}
	processDone := make(chan error, 1)
	go func() { processDone <- command.Wait() }()
	t.Cleanup(func() { _ = command.Process.Kill(); <-processDone })
	baseURL := fmt.Sprintf("http://127.0.0.1:%d", port)
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.NewTimer(20 * time.Second)
	defer deadline.Stop()
	for {
		request, _ := http.NewRequest("GET", baseURL+"/v1/models", nil)
		request.Header.Set("Authorization", "Bearer integration-client")
		response, errGet := http.DefaultClient.Do(request)
		if errGet == nil {
			_, _ = io.Copy(io.Discard, response.Body)
			_ = response.Body.Close()
			if response.StatusCode == 200 {
				break
			}
		}
		select {
		case <-ticker.C:
		case <-deadline.C:
			t.Fatalf("main did not become ready:\n%s", log.String())
		}
	}
	return &mainFixture{baseURL, captured, log}
}

func (fixture *mainFixture) post(t *testing.T, path string, body []byte) []byte {
	t.Helper()
	request, errNew := http.NewRequest("POST", fixture.url+path, bytes.NewReader(body))
	if errNew != nil {
		t.Fatal(errNew)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer integration-client")
	response, errPost := http.DefaultClient.Do(request)
	if errPost != nil {
		t.Fatal(errPost)
	}
	defer func() { _ = response.Body.Close() }()
	answer, errRead := io.ReadAll(response.Body)
	if errRead != nil {
		t.Fatal(errRead)
	}
	if response.StatusCode != 200 {
		t.Fatalf("HTTP %d: %s\nmain log:\n%s", response.StatusCode, answer, fixture.log.String())
	}
	return answer
}

func TestMainLoadsPluginAndReturnsEmulatedTools(t *testing.T) {
	fixture := startMainFixture(t)
	body := fixture.post(t, "/v1/chat/completions", []byte(chatWeatherRequest))
	if ojson.NewDoc(body).Get("choices.0.message.tool_calls.0.function.name").String() != "get_weather" {
		t.Fatalf("not an emulated tool call: %s\n%s", body, fixture.log.String())
	}
	upstream := <-fixture.captured
	if ojson.NewDoc(upstream.body).Get("tools").Exists() {
		t.Fatalf("native tools leaked upstream: %s", upstream.body)
	}
}

func TestMainStreamingKeepsNativeToolSSE(t *testing.T) {
	fixture := startMainFixture(t)
	doc := ojson.NewDoc([]byte(chatWeatherRequest))
	_ = doc.Set("stream", true)
	answer := fixture.post(t, "/v1/chat/completions", doc.Bytes())
	if !strings.Contains(string(answer), `"tool_calls"`) || !strings.Contains(string(answer), "data: [DONE]") {
		t.Fatalf("invalid native tool SSE: %s", answer)
	}
}

func TestMainClaudePreservesCacheBreakpointAndStablePrefix(t *testing.T) {
	fixture := startMainFixture(t)
	var prefix string
	for _, choice := range []string{"required", "auto"} {
		doc := ojson.NewDoc([]byte(chatWeatherRequest))
		_ = doc.Set("model", "claude-model")
		_ = doc.Set("tool_choice", choice)
		fixture.post(t, "/v1/chat/completions", doc.Bytes())
		request := <-fixture.captured
		upstream := ojson.NewDoc(request.body)
		part := upstream.Get("messages.0.content.0")
		if part.Get("cache_control.type").String() != "ephemeral" {
			t.Fatalf("stable block lost cache marker: %s", request.body)
		}
		if prefix != "" && prefix != part.Get("text").String() {
			t.Fatal("prepared native Claude prefix changed")
		}
		prefix = part.Get("text").String()
		if upstream.Get("prompt_cache_options").Exists() {
			t.Fatal("host-only cache option leaked onto the wire")
		}
	}
}

func TestMainAppliesPayloadArrayDeletionOncePerAttempt(t *testing.T) {
	fixture := startMainFixture(t)
	doc := ojson.NewDoc([]byte(chatWeatherRequest))
	_ = doc.Set("model", "filter-model")
	_ = doc.Set("messages", []any{map[string]any{"role": "system", "content": "system"}, map[string]any{"role": "user", "content": "remove-me"}, map[string]any{"role": "user", "content": "keep-me"}})
	fixture.post(t, "/v1/chat/completions", doc.Bytes())
	request := <-fixture.captured
	messages := ojson.NewDoc(request.body).Get("messages").Array()
	if len(messages) != 2 || messages[1].Get("content.0.text").String() != "keep-me" {
		t.Fatalf("payload deletion was not applied exactly once: %s", request.body)
	}
}
