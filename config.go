package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/MoeForge/moego/json"
	"github.com/MoeForge/moego/regexp"
	"github.com/jianhongyu136/plugin-tool-emulation/internal/toolemu"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	"gopkg.in/yaml.v3"
)

type ruleConfig struct {
	Provider     string   `yaml:"provider"`
	Models       []string `yaml:"models"`
	ModelAliases []string `yaml:"model_aliases"`
}

type pluginConfig struct {
	Enabled        bool                          `yaml:"enabled"`
	Priority       int                           `yaml:"priority"`
	Rules          []ruleConfig                  `yaml:"rules"`
	ParseRetry     int                           `yaml:"parse_retry"`
	OnParseFailure string                        `yaml:"on_parse_failure"`
	FenceToken     string                        `yaml:"fence_token"`
	TagGroup       toolemu.ToolEmulationTagGroup `yaml:"tag_group"`
	PrefixCache    bool                          `yaml:"prefix_cache"`
	CacheTTL       string                        `yaml:"cache_ttl"`
}

type compiledRule struct {
	provider string
	models   []*regexp.Regexp
	aliases  []*regexp.Regexp
}

type runtimeConfig struct {
	config pluginConfig
	rules  []compiledRule
}

func parseConfig(raw []byte) (*runtimeConfig, error) {
	var request struct {
		SchemaVersion uint32 `json:"schema_version"`
		ConfigYAML    []byte `json:"config_yaml"`
	}
	if errDecode := json.Unmarshal(raw, &request); errDecode != nil {
		return nil, errors.New("invalid lifecycle request")
	}
	if request.SchemaVersion < pluginabi.SchemaVersion {
		return nil, fmt.Errorf("schema_version must be at least %d", pluginabi.SchemaVersion)
	}
	cfg := pluginConfig{ParseRetry: 1, OnParseFailure: "parse_failed_to_content", PrefixCache: true, CacheTTL: "5m"}
	if len(request.ConfigYAML) > 0 {
		decoder := yaml.NewDecoder(bytes.NewReader(request.ConfigYAML))
		if errDecode := decoder.Decode(&cfg); errDecode != nil {
			return nil, fmt.Errorf("invalid config_yaml: %w", errDecode)
		}
		var extra any
		if errExtra := decoder.Decode(&extra); !errors.Is(errExtra, io.EOF) {
			return nil, errors.New("config_yaml must contain one YAML document")
		}
	}
	if cfg.ParseRetry < 0 || cfg.ParseRetry > 8 {
		return nil, errors.New("parse_retry must be between 0 and 8")
	}
	if cfg.OnParseFailure != "error" && cfg.OnParseFailure != "parse_failed_to_content" {
		return nil, errors.New("on_parse_failure must be error or parse_failed_to_content")
	}
	if cfg.CacheTTL != "5m" && cfg.CacheTTL != "1h" {
		return nil, errors.New("cache_ttl must be 5m or 1h")
	}
	if cfg.FenceToken != "" && !regexp.MustCompile(`^[A-Za-z0-9_-]{4,32}$`).MatchString(cfg.FenceToken) {
		return nil, errors.New("invalid fence delimiter; use 4-32 ASCII letters, digits, underscores or hyphens")
	}
	for _, tag := range []string{cfg.TagGroup.Tool, cfg.TagGroup.Arg, cfg.TagGroup.Result} {
		if tag != "" && !regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]{0,63}$`).MatchString(tag) {
			return nil, errors.New("invalid protocol tag name")
		}
	}
	protocol := (toolemu.ToolEmulationConfig{FenceToken: cfg.FenceToken, TagGroup: cfg.TagGroup}).DefaultsApplied()
	cfg.FenceToken, cfg.TagGroup = protocol.FenceToken, protocol.TagGroup
	out := &runtimeConfig{config: cfg}
	for _, rule := range cfg.Rules {
		compiled := compiledRule{provider: strings.TrimSpace(rule.Provider)}
		for _, pattern := range rule.Models {
			compiled.models = append(compiled.models, compileGlob(pattern))
		}
		for _, pattern := range rule.ModelAliases {
			compiled.aliases = append(compiled.aliases, compileGlob(pattern))
		}
		out.rules = append(out.rules, compiled)
	}
	return out, nil
}

func compileGlob(pattern string) *regexp.Regexp {
	pattern = regexp.QuoteMeta(strings.TrimSpace(pattern))
	pattern = strings.ReplaceAll(strings.ReplaceAll(pattern, `\*`, `.*`), `\?`, `.`)
	return regexp.MustCompile("^" + pattern + "$")
}

func (cfg *runtimeConfig) selectRule(model, alias string) (string, bool) {
	if cfg == nil || !cfg.config.Enabled {
		return "", false
	}
	for _, rule := range cfg.rules {
		for _, pattern := range rule.models {
			if pattern.MatchString(model) || pattern.MatchString(alias) {
				return rule.provider, true
			}
		}
		for _, pattern := range rule.aliases {
			if pattern.MatchString(alias) {
				return rule.provider, true
			}
		}
	}
	return "", false
}

func configFields() []pluginapi.ConfigField {
	return []pluginapi.ConfigField{
		{Name: "rules", Type: pluginapi.ConfigFieldTypeArray, Description: "First matching rule: {provider?, models?: [glob], model_aliases?: [glob]}. Empty rules do not intercept requests; use an explicit * to match all models. An omitted provider uses native routing."},
		{Name: "parse_retry", Type: pluginapi.ConfigFieldTypeInteger, Description: "Additional non-streaming parse attempts, from 0 to 8; default 1."},
		{Name: "on_parse_failure", Type: pluginapi.ConfigFieldTypeEnum, EnumValues: []string{"parse_failed_to_content", "error"}, Description: "Syntax-error fallback after retry exhaustion. Tool-choice violations always return an error."},
		{Name: "fence_token", Type: pluginapi.ConfigFieldTypeString, Description: "Optional fixed raw-protocol delimiter. Never generate a delimiter per request."},
		{Name: "tag_group", Type: pluginapi.ConfigFieldTypeObject, Description: "Optional raw protocol tag names: {tool, arg, result}."},
		{Name: "prefix_cache", Type: pluginapi.ConfigFieldTypeBoolean, Description: "Keep deterministic prompt prefixes and explicitly cache the Claude protocol block; default true."},
		{Name: "cache_ttl", Type: pluginapi.ConfigFieldTypeEnum, EnumValues: []string{"5m", "1h"}, Description: "Claude cache TTL; default 5m. Upstream support and minimum cache length still apply."},
	}
}
