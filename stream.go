package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"

	"github.com/MoeForge/moego/ojson"
	"github.com/jianhongyu136/plugin-tool-emulation/internal/toolemu"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

type hostStreamReader struct {
	plugin     *Plugin
	id         string
	callbackID string
	pending    []byte
	done       bool
	closeOnce  sync.Once
}

func (reader *hostStreamReader) Read(out []byte) (int, error) {
	for len(reader.pending) == 0 && !reader.done {
		var response pluginapi.HostModelStreamReadResponse
		if errRead := reader.plugin.callHost(pluginabi.MethodHostModelStreamRead, map[string]any{"stream_id": reader.id, "host_callback_id": reader.callbackID}, &response); errRead != nil {
			return 0, errRead
		}
		if response.Error != "" {
			return 0, errors.New(response.Error)
		}
		reader.pending = response.Payload
		// Native model callbacks yield JSON chunks; the protocol pump consumes SSE.
		data := bytes.TrimSpace(reader.pending)
		if (bytes.HasPrefix(data, []byte("{")) && ojson.ValidBytes(data)) || bytes.Equal(data, []byte("[DONE]")) {
			reader.pending = append(append([]byte("data: "), data...), '\n', '\n')
		}
		reader.done = response.Done
	}
	if len(reader.pending) == 0 {
		return 0, io.EOF
	}
	count := copy(out, reader.pending)
	reader.pending = reader.pending[count:]
	return count, nil
}

func (reader *hostStreamReader) Close() {
	reader.closeOnce.Do(func() {
		_ = reader.plugin.callHost(pluginabi.MethodHostModelStreamClose, map[string]any{"stream_id": reader.id, "host_callback_id": reader.callbackID}, nil)
	})
}

func (p *Plugin) executeStream(raw []byte) ([]byte, error) {
	prepared, errPrepare := p.prepare(raw)
	if errPrepare != nil {
		return failure("invalid_request", errPrepare.Error(), http.StatusBadRequest)
	}
	id := strings.TrimSpace(prepared.request.StreamID)
	if id == "" {
		return failure("invalid_request", "stream_id is required", http.StatusBadRequest)
	}
	var response pluginapi.HostModelStreamResponse
	if errStart := p.callHost(pluginabi.MethodHostModelExecuteStream, prepared.modelRequest(prepared.folded, true), &response); errStart != nil {
		return failure("upstream_error", errStart.Error(), errorStatus(errStart))
	}
	if response.StreamID == "" {
		return failure("upstream_error", "native upstream returned an empty stream handle", http.StatusBadGateway)
	}
	reader := &hostStreamReader{plugin: p, id: response.StreamID, callbackID: prepared.request.HostCallbackID}
	ctx, cancel := context.WithCancel(context.Background())
	p.mu.Lock()
	if p.closing {
		p.mu.Unlock()
		cancel()
		reader.Close()
		return failure("plugin_stopping", "plugin is stopping", http.StatusServiceUnavailable)
	}
	if _, duplicate := p.streams[id]; duplicate {
		p.mu.Unlock()
		cancel()
		reader.Close()
		return failure("invalid_request", "stream_id is already active", http.StatusConflict)
	}
	p.streams[id] = cancel
	p.workers.Add(1)
	p.mu.Unlock()
	finished := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			reader.Close()
		case <-finished:
		}
	}()
	go func() {
		defer p.workers.Done()
		var runError error
		defer func() {
			if recover() != nil {
				runError = errors.New("tool-emulation stream failed")
			}
			reader.Close()
			close(finished)
			cancel()
			p.mu.Lock()
			delete(p.streams, id)
			p.mu.Unlock()
			errorText := ""
			if runError != nil {
				errorText = runError.Error()
			}
			_ = p.callHost(pluginabi.MethodHostStreamClose, map[string]any{"stream_id": id, "error": errorText}, nil)
		}()
		meta := toolemu.UpstreamMeta{Provider: prepared.provider, Model: prepared.request.Model}
		emitter := toolemu.NewChatStreamEmitter(meta, func(frame []byte) {
			if runError != nil {
				return
			}
			if errEmit := p.callHost(pluginabi.MethodHostStreamEmit, map[string]any{"stream_id": id, "payload": frame}, nil); errEmit != nil {
				runError = errEmit
				cancel()
			}
		})
		emitter.SetToolChoice(prepared.choice)
		events := emitter.Events()
		originalError := events.OnError
		events.OnError = func(err error) {
			if originalError != nil {
				originalError(err)
			}
			runError = err
		}
		var parser *toolemu.StreamParser
		originalStart := events.OnToolCallStart
		events.OnToolCallStart = func(index int, callID, name string) {
			parsed := toolemu.Parsed{ToolCalls: []toolemu.ParsedToolCall{{Name: name}}}
			if errName := toolemu.ValidateDeclaredToolNames(parsed, prepared.policy.Tools); errName != nil {
				parser.Abort(errName)
				return
			}
			if originalStart != nil {
				originalStart(index, callID, name)
			}
		}
		parser = toolemu.NewStreamParserWithTagGroup(events, meta, prepared.cfg.config.FenceToken, prepared.cfg.config.TagGroup)
		parser.SetToolSpecs(prepared.policy.Tools)
		pump := toolemu.UpstreamPump{Reader: reader, Shape: toolemu.ShapeOpenAIChat, Parser: parser}
		_, errPump := pump.Run(ctx)
		if runError == nil {
			runError = errPump
		}
		if runError == nil {
			runError = emitter.Err()
		}
	}()
	return success(map[string]any{"headers": http.Header{"Content-Type": {"text/event-stream"}}})
}
