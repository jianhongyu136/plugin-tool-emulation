//go:build integration

package main

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MoeForge/moego/ojson"
)

func TestMainPreservesContentInFinishingToolDelta(t *testing.T) {
	fixture := startMainFixtureWithOptions(t, mainFixtureOptions{streamHandler: func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = writer.Write(chatStreamFrame(t, nativeWeatherText+"\n", "", "stop"))
		_, _ = io.WriteString(writer, "data: [DONE]\n\n")
	}})
	doc := ojson.NewDocString(chatWeatherRequest)
	if err := doc.Set("stream", true); err != nil {
		t.Fatal(err)
	}
	answer := string(fixture.post(t, "/v1/chat/completions", doc.Bytes()))
	if !strings.Contains(answer, `"name":"get_weather"`) || !strings.Contains(answer, `\"city\":\"Tokyo\"`) || !strings.Contains(answer, "data: [DONE]") || strings.Contains(answer, "<CPA_TC") {
		t.Fatalf("final tool delta was lost or leaked: %s", answer)
	}
}

func TestMainStreamsProseBeforeUpstreamCompletion(t *testing.T) {
	release := make(chan struct{})
	var releaseOnce sync.Once
	fixture := startMainFixtureWithOptions(t, mainFixtureOptions{streamHandler: func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = writer.Write(chatStreamFrame(t, "first live paragraph", "", ""))
		writer.(http.Flusher).Flush()
		select {
		case <-release:
		case <-request.Context().Done():
			return
		}
		_, _ = writer.Write(chatStreamFrame(t, "", "", "stop"))
		_, _ = io.WriteString(writer, "data: [DONE]\n\n")
	}})
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	doc := ojson.NewDocString(chatWeatherRequest)
	if err := doc.Set("stream", true); err != nil {
		t.Fatal(err)
	}
	if err := doc.Set("tool_choice", "auto"); err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequestWithContext(ctx, "POST", fixture.url+"/v1/chat/completions", bytes.NewReader(doc.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+"integration-client")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("no response before upstream completion: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatalf("stream HTTP status=%d", response.StatusCode)
	}
	scanner := bufio.NewScanner(response.Body)
	seen := false
	for scanner.Scan() {
		if strings.Contains(scanner.Text(), "first live paragraph") {
			seen = true
			break
		}
	}
	if !seen {
		t.Fatalf("no prose before upstream was released: %v", scanner.Err())
	}
	releaseOnce.Do(func() { close(release) })
	var remaining strings.Builder
	for scanner.Scan() {
		remaining.WriteString(scanner.Text())
		remaining.WriteByte('\n')
	}
	if scanner.Err() != nil || !strings.Contains(remaining.String(), "data: [DONE]") {
		t.Fatalf("stream did not finish cleanly: %v; %s", scanner.Err(), remaining.String())
	}
}

func TestMainGeminiAlternativeSchemaKeepsNumericArguments(t *testing.T) {
	fixture := startMainFixtureWithOptions(t, mainFixtureOptions{replyText: func([]byte) string { return strings.Replace(nativeWeatherText, "Tokyo", "42", 1) }})
	doc := ojson.NewDocString(formatWeatherRequests["gemini"])
	if err := doc.Delete("tools.0.functionDeclarations.0.parameters"); err != nil {
		t.Fatal(err)
	}
	if err := doc.SetRaw("tools.0.functionDeclarations.0.parametersJsonSchema", `{"type":"object","properties":{"city":{"type":"integer"}}}`); err != nil {
		t.Fatal(err)
	}
	answer := fixture.post(t, "/v1beta/models/test-model:generateContent", doc.Bytes())
	city := ojson.NewDoc(answer).Get("candidates.0.content.parts.0.functionCall.args.city")
	if city.Type != ojson.Number || city.Int() != 42 {
		t.Fatalf("Gemini lost integer schema: %s", answer)
	}
}

func TestMainStreamBufferLimitReturnsErrorInsteadOfTruncatedArguments(t *testing.T) {
	fixture := startMainFixtureWithOptions(t, mainFixtureOptions{maxStreamBufferBytes: 1024, replyText: func([]byte) string { return strings.Replace(nativeWeatherText, "Tokyo", strings.Repeat("x", 1500), 1) }})
	doc := ojson.NewDocString(chatWeatherRequest)
	if err := doc.Set("stream", true); err != nil {
		t.Fatal(err)
	}
	answer := string(fixture.post(t, "/v1/chat/completions", doc.Bytes()))
	if !strings.Contains(answer, "buffer limit") || strings.Contains(answer, `"finish_reason":"tool_calls"`) || strings.Contains(answer, strings.Repeat("x", 100)) {
		t.Fatalf("buffer limit did not fail safely: %s", answer)
	}
}
