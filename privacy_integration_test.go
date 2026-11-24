//go:build integration

package main

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/MoeForge/moego/ojson"
	"github.com/MoeForge/moego/regexp"
)

func TestMainPrivacyAndEmulationRestoreToolArguments(t *testing.T) {
	marker := "BOUNDARY-ABCDEFGHIJKL"
	opaquePattern := regexp.MustCompile(`<PRIVATE_[0-9a-f]{16}>`)
	for _, priority := range []int{-100, 0, 100} {
		t.Run(fmt.Sprintf("priority-%d", priority), func(t *testing.T) {
			fixture := startMainFixtureWithOptions(t, mainFixtureOptions{privacyMode: "filter", privacyPriority: priority, privacyBuiltins: true, replyText: func(body []byte) string {
				opaqueValue := opaquePattern.FindString(decodedChatContent(body))
				if opaqueValue == "" {
					return nativeWeatherText
				}
				return strings.ReplaceAll(nativeWeatherText, "Tokyo", opaqueValue)
			}})
			if !strings.Contains(fixture.log.String(), "plugin_id=privacy-filter") || !strings.Contains(fixture.log.String(), "plugin_id=tool-emulation") {
				t.Fatalf("both plugins must load: %s", fixture.log.String())
			}
			for _, format := range []string{"openai", "openai-response", "claude", "gemini"} {
				for _, stream := range []bool{false, true} {
					t.Run(fmt.Sprintf("%s/stream-%t", format, stream), func(t *testing.T) {
						doc, path := privacyRequest(t, format, marker, stream)
						response := fixture.post(t, path, doc.Bytes())
						captured := <-fixture.captured
						if bytes.Contains(captured.body, []byte(marker)) || !opaquePattern.MatchString(decodedChatContent(captured.body)) || ojson.NewDoc(captured.body).Get("tools").Exists() {
							t.Fatalf("combined plugin upstream boundary mismatch: %s", captured.body)
						}
						if !bytes.Contains(response, []byte(marker)) || opaquePattern.Match(response) || !bytes.Contains(response, []byte("get_weather")) {
							t.Fatalf("privacy/emulation failed to restore %s tool arguments: %s", format, response)
						}
						if stream && format == "openai" && !bytes.Contains(response, []byte("[DONE]")) {
							t.Fatalf("incomplete restored stream: %s", response)
						}
					})
				}
			}
		})
	}
}

func privacyRequest(t *testing.T, format, marker string, stream bool) (*ojson.Doc, string) {
	t.Helper()
	doc := ojson.NewDocString(formatWeatherRequests[format])
	field, path := "messages.0.content", "/v1/chat/completions"
	switch format {
	case "openai-response":
		field, path = "input", "/v1/responses"
	case "claude":
		field, path = "messages.0.content.0.text", "/v1/messages"
	case "gemini":
		field, path = "contents.0.parts.0.text", "/v1beta/models/test-model:generateContent"
	}
	if errSet := doc.Set(field, "Use get_weather for "+marker); errSet != nil {
		t.Fatal(errSet)
	}
	if stream {
		if format == "gemini" {
			path = "/v1beta/models/test-model:streamGenerateContent?alt=sse"
		} else if errSet := doc.Set("stream", true); errSet != nil {
			t.Fatal(errSet)
		}
	}
	return doc, path
}

func TestMainPrivacyBlockStopsEmulationBeforeUpstream(t *testing.T) {
	fixture := startMainFixtureWithOptions(t, mainFixtureOptions{privacyMode: "block", privacyPriority: 100})
	for _, format := range []string{"openai", "openai-response", "claude", "gemini"} {
		t.Run(format, func(t *testing.T) {
			doc, path := privacyRequest(t, format, "BOUNDARY-ABCDEFGHIJKL", false)
			request, errNew := http.NewRequest("POST", fixture.url+path, bytes.NewReader(doc.Bytes()))
			if errNew != nil {
				t.Fatal(errNew)
			}
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Authorization", "Bearer "+"integration-client")
			response, errPost := http.DefaultClient.Do(request)
			if errPost != nil {
				t.Fatal(errPost)
			}
			defer func() { _ = response.Body.Close() }()
			body, errRead := io.ReadAll(response.Body)
			if errRead != nil {
				t.Fatal(errRead)
			}
			if response.StatusCode < 400 || bytes.Contains(body, []byte("BOUNDARY-ABCDEFGHIJKL")) {
				t.Fatalf("block mode did not safely reject: HTTP %d: %s", response.StatusCode, body)
			}
			select {
			case captured := <-fixture.captured:
				t.Fatalf("blocked request reached upstream: %s", captured.body)
			default:
			}
		})
	}
}

func TestMainPrivacyConcurrentStreamsStayIsolatedAndKeepPrefix(t *testing.T) {
	opaquePattern := regexp.MustCompile(`<PRIVATE_[0-9a-f]{16}>`)
	fixture := startMainFixtureWithOptions(t, mainFixtureOptions{privacyMode: "filter", privacyPriority: 100, replyText: func(body []byte) string {
		value := opaquePattern.FindString(decodedChatContent(body))
		return strings.ReplaceAll(nativeWeatherText, "Tokyo", value)
	}})
	const requests = 16
	t.Run("requests", func(t *testing.T) {
		for index := range requests {
			t.Run(fmt.Sprintf("request-%d", index), func(t *testing.T) {
				t.Parallel()
				marker := fmt.Sprintf("BOUNDARY-%012d", index)
				doc, path := privacyRequest(t, "openai", marker, index%2 == 0)
				body := fixture.post(t, path, doc.Bytes())
				if !bytes.Contains(body, []byte(marker)) || opaquePattern.Match(body) {
					t.Fatalf("request %d was not restored: %s", index, body)
				}
				for other := range requests {
					if other != index && bytes.Contains(body, []byte(fmt.Sprintf("BOUNDARY-%012d", other))) {
						t.Fatalf("request %d leaked another request", index)
					}
				}
			})
		}
	})
	var prefix string
	for range requests {
		captured := <-fixture.captured
		if bytes.Contains(captured.body, []byte("BOUNDARY-")) {
			t.Fatal("concurrent request sent plaintext upstream")
		}
		current := ojson.NewDoc(captured.body).Get("messages.0.content.0.text").String()
		if current == "" || prefix != "" && current != prefix {
			t.Fatal("concurrent redaction changed cacheable protocol prefix")
		}
		prefix = current
	}
}

func TestMainPrivacyPreservesFoldedToolHistoryAndCachePrefix(t *testing.T) {
	fixture := startMainFixtureWithOptions(t, mainFixtureOptions{privacyMode: "filter", privacyPriority: 100})
	var prefix string
	for _, history := range []bool{false, true} {
		doc, path := privacyRequest(t, "openai", "BOUNDARY-ABCDEFGHIJKL", false)
		if history {
			for _, message := range []any{
				map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"id": "previous-call", "type": "function", "function": map[string]any{"name": "get_weather", "arguments": `{"city":"BOUNDARY-ABCDEFGHIJKL"}`}}}},
				map[string]any{"role": "tool", "tool_call_id": "previous-call", "content": "Result BOUNDARY-MNOPQRSTUVWX"},
				map[string]any{"role": "user", "content": "Get the weather again."},
			} {
				if errSet := doc.Set("messages.-1", message); errSet != nil {
					t.Fatal(errSet)
				}
			}
		}
		fixture.post(t, path, doc.Bytes())
		captured := <-fixture.captured
		if bytes.Contains(captured.body, []byte("BOUNDARY-")) {
			t.Fatalf("history plaintext leaked: %s", captured.body)
		}
		upstream := ojson.NewDoc(captured.body)
		current := upstream.Get("messages.0.content.0.text").String()
		if current == "" || prefix != "" && prefix != current {
			t.Fatal("folded history changed cacheable tool prefix")
		}
		prefix = current
		if history && !strings.Contains(decodedChatContent(captured.body), "<CPA_TR|") {
			t.Fatal("historical tool result was lost")
		}
	}
}

func TestMainPrivacyBuiltinsLeaveProtocolPrefixUnchanged(t *testing.T) {
	baseline := startMainFixture(t)
	combined := startMainFixtureWithOptions(t, mainFixtureOptions{privacyMode: "filter", privacyPriority: 100, privacyBuiltins: true})
	baseline.post(t, "/v1/chat/completions", []byte(chatWeatherRequest))
	combined.post(t, "/v1/chat/completions", []byte(chatWeatherRequest))
	first, second := <-baseline.captured, <-combined.captured
	prefix := ojson.NewDoc(first.body).Get("messages.0.content.0.text").String()
	withPrivacy := ojson.NewDoc(second.body).Get("messages.0.content.0.text").String()
	if prefix == "" || prefix != withPrivacy {
		t.Fatal("default privacy rules rewrote the tool protocol prefix")
	}
}

func decodedChatContent(body []byte) string {
	var parts []string
	ojson.NewDoc(body).Get("messages").ForEach(func(_, message ojson.Result) bool {
		content := message.Get("content")
		if content.IsArray() {
			content.ForEach(func(_, part ojson.Result) bool { parts = append(parts, part.Get("text").String()); return true })
		} else {
			parts = append(parts, content.String())
		}
		return true
	})
	return strings.Join(parts, "\n")
}
