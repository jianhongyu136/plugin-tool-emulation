package main

import (
	"fmt"
	"strings"

	"github.com/MoeForge/moego/json"
	"github.com/MoeForge/moego/ojson"
	"github.com/jianhongyu136/plugin-tool-emulation/internal/toolemu"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

const cachePragma = "<!-- tool-emulation: cache="

func setStableChoice(doc *ojson.Doc, shape toolemu.UpstreamShape) error {
	switch shape {
	case toolemu.ShapeOpenAIChat, toolemu.ShapeOpenAIResponses:
		return doc.Set("tool_choice", "auto")
	case toolemu.ShapeClaudeMessages:
		return doc.Set("tool_choice", map[string]any{"type": "auto"})
	case toolemu.ShapeGeminiGenerateContent:
		_ = doc.Delete("tool_config")
		return doc.Set("toolConfig.functionCallingConfig", map[string]any{"mode": "AUTO"})
	}
	return nil
}

func messageArray(shape toolemu.UpstreamShape) (array, content, textType string) {
	switch shape {
	case toolemu.ShapeOpenAIResponses:
		return "input", "content", "input_text"
	case toolemu.ShapeGeminiGenerateContent:
		return "contents", "parts", ""
	default:
		return "messages", "content", "text"
	}
}

func userIndex(doc *ojson.Doc, array string, last bool) int {
	found := -1
	doc.Get(array).ForEach(func(index, item ojson.Result) bool {
		if item.Get("role").String() == "user" {
			found = int(index.Int())
			if !last {
				return false
			}
		}
		return true
	})
	return found
}

func appendTurnChoice(doc *ojson.Doc, shape toolemu.UpstreamShape, choice toolemu.ToolChoice) error {
	if choice.Kind == toolemu.ToolChoiceKindAuto && !choice.DisableParallel {
		return nil
	}
	instruction := "Tool constraints for THIS response (override earlier examples): "
	switch choice.Kind {
	case toolemu.ToolChoiceKindNone:
		instruction += "Do not call any tool. Answer in natural language only."
	case toolemu.ToolChoiceKindRequired:
		instruction += "You MUST emit at least one tool call."
	case toolemu.ToolChoiceKindNamed:
		instruction += fmt.Sprintf("You MUST call the tool named %q, and no other tool.", choice.Name)
	default:
		instruction += "Call tools only when needed."
	}
	if choice.DisableParallel {
		instruction += " Emit at most one tool call."
	}
	array, content, textType := messageArray(shape)
	index := userIndex(doc, array, true)
	part := map[string]any{"text": instruction}
	if textType != "" {
		part["type"] = textType
	}
	if index < 0 {
		return doc.Set(array+".-1", map[string]any{"role": "user", content: []any{part}})
	}
	path := fmt.Sprintf("%s.%d.%s", array, index, content)
	value := doc.Get(path)
	if value.Type == ojson.String {
		previous := map[string]any{"type": textType, "text": value.String()}
		return doc.Set(path, []any{previous, part})
	}
	return doc.Set(path+".-1", part)
}

func stampPrefix(doc *ojson.Doc, shape toolemu.UpstreamShape, cfg pluginConfig) error {
	array, content, textType := messageArray(shape)
	if shape == toolemu.ShapeGeminiGenerateContent {
		parts := doc.Get("systemInstruction.parts")
		var injection ojson.Result
		index := -1
		parts.ForEach(func(key, part ojson.Result) bool {
			if strings.HasPrefix(part.Get("text").String(), "<tools_doc>\n") {
				injection, index = part, int(key.Int())
			}
			return true
		})
		if index >= 0 {
			if errDelete := doc.Delete(fmt.Sprintf("systemInstruction.parts.%d", index)); errDelete != nil {
				return errDelete
			}
			if len(parts.Array()) == 1 {
				_ = doc.Delete("systemInstruction")
			}
			user := userIndex(doc, array, false)
			if user < 0 {
				user = 0
				if errSet := doc.Set(array+".-1", map[string]any{"role": "user", content: []any{}}); errSet != nil {
					return errSet
				}
			}
			path := fmt.Sprintf("%s.%d.%s", array, user, content)
			merged := []any{json.RawMessage(injection.Raw)}
			doc.Get(path).ForEach(func(_, part ojson.Result) bool { merged = append(merged, json.RawMessage(part.Raw)); return true })
			if errSet := doc.Set(path, merged); errSet != nil {
				return errSet
			}
		}
	}
	var prefixPaths []string
	doc.Get(array).ForEach(func(key, message ojson.Result) bool {
		base := fmt.Sprintf("%s.%d.%s", array, key.Int(), content)
		value := message.Get(content)
		if value.Type == ojson.String && strings.HasPrefix(value.String(), "<tools_doc>\n") {
			// Split a legacy string injection into a stable block and the original text.
			text := value.String()
			end := strings.Index(text, "</tool_protocol>")
			if end >= 0 {
				end += len("</tool_protocol>")
				first := map[string]any{"text": text[:end]}
				if textType != "" {
					first["type"] = textType
				}
				parts := []any{first}
				if tail := strings.TrimPrefix(text[end:], "\n"); tail != "" {
					parts = append(parts, map[string]any{"type": textType, "text": tail})
				}
				_ = doc.Set(base, parts)
				prefixPaths = append(prefixPaths, base+".0")
			}
			return true
		}
		value.ForEach(func(index, part ojson.Result) bool {
			if strings.HasPrefix(part.Get("text").String(), "<tools_doc>\n") {
				prefixPaths = append(prefixPaths, fmt.Sprintf("%s.%d", base, index.Int()))
			}
			return true
		})
		return true
	})
	for _, path := range prefixPaths {
		if !cfg.PrefixCache {
			_ = doc.Delete(path + ".cache_control")
			continue
		}
		text := cachePragma + cfg.CacheTTL + " -->\n" + doc.Get(path+".text").String()
		if errSet := doc.Set(path+".text", text); errSet != nil {
			return errSet
		}
	}
	return nil
}

// normalizeCache is idempotent and runs after native translation, before provider
// preparation and the host's final payload-configuration barrier. The pragma pins
// the originating configuration even if a reconfigure occurs during execution.
func (p *Plugin) normalizeCache(raw []byte) ([]byte, error) {
	var request pluginapi.RequestTransformRequest
	if errDecode := json.Unmarshal(raw, &request); errDecode != nil {
		return failure("invalid_request", "invalid normalization request", 400)
	}
	if request.ToFormat != "claude" {
		return success(map[string]any{})
	}
	doc := ojson.NewDoc(request.Body)
	var prefix, ttl, rolling string
	var oldMarkers []string
	doc.Get("system").ForEach(func(index, block ojson.Result) bool {
		if block.Get("cache_control").Exists() {
			oldMarkers = append(oldMarkers, fmt.Sprintf("system.%d.cache_control", index.Int()))
		}
		return true
	})
	doc.Get("messages").ForEach(func(index, message ojson.Result) bool {
		message.Get("content").ForEach(func(partIndex, part ojson.Result) bool {
			path := fmt.Sprintf("messages.%d.content.%d", index.Int(), partIndex.Int())
			if part.Get("cache_control").Exists() {
				oldMarkers = append(oldMarkers, path+".cache_control")
			}
			text := part.Get("text").String()
			if part.Get("type").String() == "text" {
				rolling = path
			}
			for _, candidate := range []string{"5m", "1h"} {
				if strings.HasPrefix(text, cachePragma+candidate+" -->\n<tools_doc>\n") {
					prefix, ttl = path, candidate
				}
			}
			return true
		})
		return true
	})
	if prefix == "" {
		return success(map[string]any{})
	}
	for _, path := range oldMarkers {
		_ = doc.Delete(path)
	}
	_ = doc.Delete("cache_control")
	marker := map[string]any{"type": "ephemeral", "ttl": ttl}
	if errSet := doc.Set(prefix+".cache_control", marker); errSet != nil {
		return failure("cache_error", "could not mark cache prefix", 502)
	}
	if rolling != "" && rolling != prefix {
		_ = doc.Set(rolling+".cache_control", marker)
	}
	if errSet := doc.Set("prompt_cache_options.mode", "explicit"); errSet != nil {
		return failure("cache_error", "could not enable explicit cache mode", 502)
	}
	return success(map[string]any{"Body": doc.Bytes()})
}
