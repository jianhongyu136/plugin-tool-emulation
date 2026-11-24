package toolemu_test

import (
	"strings"
	"testing"

	"github.com/jianhongyu136/plugin-tool-emulation/internal/toolemu"
)

type choiceEmitter interface {
	SetToolChoice(toolemu.ToolChoice)
	Events() toolemu.StreamEvents
}

func TestAllStreamEmittersEnforceAllowedNames(t *testing.T) {
	factories := map[string]func(func([]byte)) choiceEmitter{
		"chat": func(emit func([]byte)) choiceEmitter {
			return toolemu.NewChatStreamEmitter(toolemu.UpstreamMeta{}, emit)
		},
		"claude": func(emit func([]byte)) choiceEmitter {
			return toolemu.NewClaudeStreamEmitter(toolemu.UpstreamMeta{}, emit)
		},
		"responses": func(emit func([]byte)) choiceEmitter {
			return toolemu.NewResponsesStreamEmitter(toolemu.UpstreamMeta{}, emit)
		},
	}
	for name, factory := range factories {
		t.Run(name, func(t *testing.T) {
			var output strings.Builder
			emitter := factory(func(frame []byte) { output.Write(frame) })
			emitter.SetToolChoice(toolemu.ToolChoice{Kind: toolemu.ToolChoiceKindRequired, AllowedNames: []string{"get_weather", "get_time"}})
			events := emitter.Events()
			events.OnToolCallStart(0, "call-test", "excluded_tool")
			events.OnArgsDelta(0, "{}")
			events.OnToolCallEnd(0)
			events.OnComplete()
			if !strings.Contains(output.String(), "allowedFunctionNames") || strings.Contains(output.String(), `"name":"excluded_tool"`) {
				t.Errorf("excluded name was not rejected: %s", output.String())
			}
		})
	}
}
