package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/MoeForge/moego/json"
	"github.com/MoeForge/moego/ojson"
	"github.com/jianhongyu136/plugin-tool-emulation/internal/toolemu"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

const pluginIdentifier = "tool-emulation"

var pluginVersion = "dev"

// HostCall is the host's public JSON callback Interface, not an HTTP transport.
type HostCall func(method string, request []byte) ([]byte, error)

// Plugin owns configuration and execution behind the native JSON ABI seam.
type Plugin struct {
	call    HostCall
	config  atomic.Pointer[runtimeConfig]
	mu      sync.Mutex
	streams map[string]context.CancelFunc
	workers sync.WaitGroup
	closing bool
	webBase atomic.Pointer[string]
}

func NewPlugin(call HostCall) *Plugin {
	return &Plugin{call: call, streams: make(map[string]context.CancelFunc)}
}

var nativePlugin = NewPlugin(nil)

type rpcExecutorRequest struct {
	pluginapi.ExecutorRequest
	HostCallbackID string `json:"host_callback_id"`
	StreamID       string `json:"stream_id"`
}

type modelExecutionRequest struct {
	pluginapi.HostModelExecutionRequest
	HostCallbackID string `json:"host_callback_id"`
}

type registration struct {
	SchemaVersion uint32             `json:"schema_version"`
	Metadata      pluginapi.Metadata `json:"metadata"`
	Capabilities  struct {
		ModelRouter            bool     `json:"model_router"`
		Executor               bool     `json:"executor"`
		ExecutorModelScope     string   `json:"executor_model_scope"`
		ExecutorInputFormats   []string `json:"executor_input_formats"`
		ExecutorOutputFormats  []string `json:"executor_output_formats"`
		RequestNormalizer      bool     `json:"request_normalizer"`
		RequestLifecyclePlugin bool     `json:"request_lifecycle_plugin"`
		ManagementAPI          bool     `json:"management_api"`
	} `json:"capabilities"`
}

func pluginRegistration() registration {
	result := registration{SchemaVersion: pluginabi.SchemaVersion, Metadata: pluginapi.Metadata{
		Name: "Tool Emulation", Version: pluginVersion, Author: "jhy", GitHubRepository: "https://github.com/jianhongyu136/plugin-tool-emulation", ConfigFields: configFields(),
	}}
	result.Capabilities.ModelRouter = true
	result.Capabilities.Executor = true
	result.Capabilities.ExecutorModelScope = string(pluginapi.ExecutorModelScopeStatic)
	result.Capabilities.ExecutorInputFormats = []string{"openai", "openai-response", "claude", "gemini"}
	result.Capabilities.ExecutorOutputFormats = []string{"openai"}
	result.Capabilities.RequestNormalizer = true
	result.Capabilities.ManagementAPI = true
	return result
}

func success(value any) ([]byte, error) {
	return json.Marshal(struct {
		OK     bool `json:"ok"`
		Result any  `json:"result,omitempty"`
	}{true, value})
}

func failure(code, message string, status int) ([]byte, error) {
	return pluginabi.NewErrorEnvelope(code, message, status)
}

// Handle is exactly the method/payload Interface driven by cliproxyPluginCall.
func (p *Plugin) Handle(method string, raw []byte) (out []byte, err error) {
	defer func() {
		if recover() != nil {
			out, err = failure("plugin_panic", "tool-emulation operation failed", http.StatusInternalServerError)
		}
	}()
	switch method {
	case pluginabi.MethodPluginRegister, pluginabi.MethodPluginReconfigure:
		cfg, errConfig := parseConfig(raw)
		if errConfig != nil {
			return failure("invalid_config", errConfig.Error(), http.StatusBadRequest)
		}
		p.config.Store(cfg)
		return success(pluginRegistration())
	case pluginabi.MethodModelRoute:
		var request pluginapi.ModelRouteRequest
		if errDecode := json.Unmarshal(raw, &request); errDecode != nil {
			return failure("invalid_request", "invalid model route request", http.StatusBadRequest)
		}
		_, matched := p.config.Load().selectRule(request.RequestedModel, request.RequestedModel)
		_, supported := sourceShape(request.SourceFormat)
		if !matched || !supported || !toolemu.HasToolArtifacts(request.Body) {
			return success(pluginapi.ModelRouteResponse{Handled: false})
		}
		return success(pluginapi.ModelRouteResponse{Handled: true, TargetKind: pluginapi.ModelRouteTargetSelf, Reason: "tool_emulation"})
	case pluginabi.MethodExecutorIdentifier:
		return success(map[string]string{"identifier": pluginIdentifier})
	case pluginabi.MethodExecutorExecute:
		return p.execute(raw)
	case pluginabi.MethodExecutorExecuteStream:
		return p.executeStream(raw)
	case pluginabi.MethodRequestNormalize:
		return p.normalizeCache(raw)
	case pluginabi.MethodManagementRegister:
		return p.registerManagement(raw)
	case pluginabi.MethodManagementHandle:
		return p.serveManagementResource(raw)
	case pluginabi.MethodPluginQuiesce, pluginabi.MethodPluginShutdown:
		p.mu.Lock()
		p.closing = true
		for _, cancel := range p.streams {
			cancel()
		}
		p.mu.Unlock()
		p.workers.Wait()
		return success(struct{}{})
	default:
		return failure("unknown_method", "unsupported tool-emulation method: "+method, http.StatusNotImplemented)
	}
}

func sourceShape(format string) (toolemu.UpstreamShape, bool) {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "openai":
		return toolemu.ShapeOpenAIChat, true
	case "openai-response":
		return toolemu.ShapeOpenAIResponses, true
	case "claude":
		return toolemu.ShapeClaudeMessages, true
	case "gemini":
		return toolemu.ShapeGeminiGenerateContent, true
	default:
		return 0, false
	}
}

func validObject(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	var value map[string]json.RawMessage
	if errDecode := decoder.Decode(&value); errDecode != nil || value == nil {
		return errors.New("request body must be a JSON object")
	}
	var extra any
	if errExtra := decoder.Decode(&extra); !errors.Is(errExtra, io.EOF) {
		return errors.New("request body must contain exactly one JSON value")
	}
	return nil
}

type preparedRequest struct {
	request  rpcExecutorRequest
	cfg      *runtimeConfig
	shape    toolemu.UpstreamShape
	provider string
	choice   toolemu.ToolChoice
	policy   toolemu.RetryPolicy
	folded   []byte
}

func (p *Plugin) prepare(raw []byte) (preparedRequest, error) {
	var request rpcExecutorRequest
	if errDecode := json.Unmarshal(raw, &request); errDecode != nil {
		return preparedRequest{}, errors.New("invalid executor request")
	}
	shape, supported := sourceShape(request.SourceFormat)
	if !supported {
		return preparedRequest{}, errors.New("unsupported source format")
	}
	if len(request.OriginalRequest) == 0 {
		return preparedRequest{}, errors.New("unconfigured OriginalRequest is required; refusing to reuse finalized Payload")
	}
	if strings.TrimSpace(request.HostCallbackID) == "" {
		return preparedRequest{}, errors.New("host_callback_id is required")
	}
	if errBody := validObject(request.OriginalRequest); errBody != nil {
		return preparedRequest{}, errBody
	}
	cfg := p.config.Load()
	alias, _ := request.Metadata["requested_model"].(string)
	if alias == "" {
		alias = request.Model
	}
	provider, matched := cfg.selectRule(request.Model, alias)
	if !matched {
		return preparedRequest{}, errors.New("executor request does not match an enabled rule")
	}
	doc := ojson.NewDoc(bytes.Clone(request.OriginalRequest))
	choice := toolemu.ExtractToolChoiceDoc(doc, shape)
	tools, errTools := toolemu.ExtractToolSpecsDoc(doc, shape)
	if errTools != nil {
		return preparedRequest{}, errTools
	}
	if cfg.config.PrefixCache && len(tools) > 0 {
		if errStable := setStableChoice(doc, shape); errStable != nil {
			return preparedRequest{}, errStable
		}
	}
	if errFold := toolemu.FoldRequestDoc(doc, toolemu.FoldOpts{Shape: shape, Provider: provider, FenceToken: cfg.config.FenceToken, TagGroup: cfg.config.TagGroup}); errFold != nil {
		return preparedRequest{}, errFold
	}
	if len(tools) > 0 {
		if errPrefix := stampPrefix(doc, shape, cfg.config); errPrefix != nil {
			return preparedRequest{}, errPrefix
		}
		if cfg.config.PrefixCache {
			if errChoice := appendTurnChoice(doc, shape, choice); errChoice != nil {
				return preparedRequest{}, errChoice
			}
		}
	}
	return preparedRequest{request: request, cfg: cfg, shape: shape, provider: provider, choice: choice,
		policy: toolemu.RetryPolicy{Attempts: cfg.config.ParseRetry, OnFailure: cfg.config.OnParseFailure, FenceToken: cfg.config.FenceToken, TagGroup: cfg.config.TagGroup, Tools: tools}, folded: doc.Bytes()}, nil
}

func (p *Plugin) callHost(method string, request any, response any) error {
	if p.call == nil {
		return errors.New("native host callbacks are unavailable")
	}
	raw, errMarshal := json.Marshal(request)
	if errMarshal != nil {
		return errMarshal
	}
	answer, errCall := p.call(method, raw)
	if errCall != nil {
		return errCall
	}
	var envelope pluginabi.Envelope
	if errDecode := json.Unmarshal(answer, &envelope); errDecode != nil {
		return errors.New("invalid host callback envelope")
	}
	if !envelope.OK {
		if envelope.Error != nil {
			return envelope.Error
		}
		return errors.New("host callback failed")
	}
	if response != nil {
		return json.Unmarshal(envelope.Result, response)
	}
	return nil
}

func (r preparedRequest) modelRequest(body []byte, stream bool) modelExecutionRequest {
	callerScope, _ := r.request.Metadata["caller_scope"].(string)
	path, _ := r.request.Metadata["request_path"].(string)
	return modelExecutionRequest{HostCallbackID: r.request.HostCallbackID, HostModelExecutionRequest: pluginapi.HostModelExecutionRequest{
		EntryProtocol: r.request.SourceFormat, ExitProtocol: "openai", Model: r.request.Model,
		Stream: stream, Body: body, Headers: r.request.Headers.Clone(), Query: r.request.Query,
		// A self-executor may receive a synthetic plugin auth, never a native pin.
		ForcedProvider: r.provider, CallerScope: callerScope, Path: path,
	}}
}

func errorStatus(err error) int {
	var status interface{ StatusCode() int }
	if errors.As(err, &status) && status.StatusCode() >= 400 {
		return status.StatusCode()
	}
	return http.StatusBadGateway
}

func (p *Plugin) execute(raw []byte) ([]byte, error) {
	prepared, errPrepare := p.prepare(raw)
	if errPrepare != nil {
		return failure("invalid_request", errPrepare.Error(), http.StatusBadRequest)
	}
	var headers http.Header
	send := func(ctx context.Context, body []byte) ([]byte, error) {
		var response pluginapi.HostModelExecutionResponse
		if errCall := p.callHost(pluginabi.MethodHostModelExecute, prepared.modelRequest(body, false), &response); errCall != nil {
			return nil, errCall
		}
		if response.StatusCode >= 400 {
			return nil, &upstreamStatusError{status: response.StatusCode}
		}
		headers = response.Headers
		return response.Body, nil
	}
	result, errParse := toolemu.ParseAndRetryFormats(context.Background(), prepared.folded, send, prepared.shape, toolemu.ShapeOpenAIChat, prepared.policy, prepared.choice)
	if errParse != nil {
		return failure("tool_emulation_error", errParse.Error(), errorStatus(errParse))
	}
	result.Meta.Provider = prepared.provider
	body, errBuild := toolemu.BuildChatCompletion(result.Parsed, result.Meta)
	if errBuild != nil {
		return failure("tool_emulation_error", "could not build tool response", http.StatusBadGateway)
	}
	return success(pluginapi.ExecutorResponse{Payload: body, Headers: headers})
}

type upstreamStatusError struct{ status int }

func (err *upstreamStatusError) Error() string {
	return fmt.Sprintf("native upstream returned HTTP %d", err.status)
}
func (err *upstreamStatusError) StatusCode() int { return err.status }
