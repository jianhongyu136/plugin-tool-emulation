//go:build integration

package main

import (
	"testing"

	"github.com/MoeForge/moego/ojson"
)

func TestMainRetriesMalformedToolsWithoutChangingPrefix(t *testing.T) {
	fixture := startMainFixture(t)
	doc := ojson.NewDoc([]byte(chatWeatherRequest))
	if errSet := doc.Set("metadata.retry_test", true); errSet != nil {
		t.Fatal(errSet)
	}
	answer := fixture.post(t, "/v1/chat/completions", doc.Bytes())
	if ojson.NewDoc(answer).Get("choices.0.message.tool_calls.0.function.name").String() != "get_weather" {
		t.Fatalf("retry did not recover tools: %s", answer)
	}
	first, second := <-fixture.captured, <-fixture.captured
	a, b := ojson.NewDoc(first.body), ojson.NewDoc(second.body)
	if a.Get("messages.0.content.0.text").String() == "" || a.Get("messages.0.content.0.text").String() != b.Get("messages.0.content.0.text").String() {
		t.Fatal("retry changed the stable tool prefix")
	}
	if len(b.Get("messages").Array()) != len(a.Get("messages").Array())+1 {
		t.Fatal("native retry was not append-only")
	}
}

func TestMainTranslatesCanonicalToolsIntoClientFormats(t *testing.T) {
	fixture := startMainFixture(t)
	cases := []struct{ format, path, resultPath string }{
		{"openai-response", "/v1/responses", "output.0.type"},
		{"claude", "/v1/messages", "content.0.type"},
		{"gemini", "/v1beta/models/test-model:generateContent", "candidates.0.content.parts.0.functionCall.name"},
	}
	for _, item := range cases {
		t.Run(item.format, func(t *testing.T) {
			answer := fixture.post(t, item.path, []byte(formatWeatherRequests[item.format]))
			value := ojson.NewDoc(answer).Get(item.resultPath).String()
			want := "get_weather"
			if item.format == "openai-response" {
				want = "function_call"
			}
			if item.format == "claude" {
				want = "tool_use"
			}
			if value != want {
				t.Fatalf("wrong %s tool response: %s", item.format, answer)
			}
			<-fixture.captured
		})
	}
}
