package toolemu_test

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/jianhongyu136/plugin-tool-emulation/internal/toolemu"
)

func TestStreamParserEmitsOrdinaryProseBeforeNewlineOrClose(t *testing.T) {
	unicodeText := "晴天☀️ Tokyo🙂"
	var unicodeChunks []string
	for _, b := range []byte(unicodeText) {
		unicodeChunks = append(unicodeChunks, string([]byte{b}))
	}
	for _, item := range []struct {
		name   string
		chunks []string
		want   string
	}{
		{"paragraph", []string{"Hello ", "world. This answer has no newline."}, "Hello world. This answer has no newline."},
		{"unicode-byte-fragments", unicodeChunks, unicodeText},
		{"literal-angle-brackets", []string{"a < b ", "and c > d"}, "a < b and c > d"},
		{"ordinary-user-word", []string{"user ", "guide"}, "user guide"},
		{"invalid-utf8", []string{string([]byte{0xff}), " replacement"}, "\uFFFD replacement"},
		{"large-single-chunk", []string{strings.Repeat("plain prose ", 20000)}, strings.Repeat("plain prose ", 20000)},
	} {
		t.Run(item.name, func(t *testing.T) {
			var prose strings.Builder
			completed := 0
			parser := toolemu.NewStreamParser(toolemu.StreamEvents{
				OnProseDelta: func(text string) { prose.WriteString(text) },
				OnComplete:   func() { completed++ },
			}, toolemu.UpstreamMeta{}, "SEED1234")
			for _, chunk := range item.chunks {
				parser.Feed(chunk)
			}
			if prose.String() != item.want {
				t.Errorf("prose not available before EOF: got %d bytes, want %d", prose.Len(), len(item.want))
			}
			if completed != 0 {
				t.Error("feeding prose prematurely completed the stream")
			}
			parser.Close()
			if prose.String() != item.want {
				t.Error("EOF duplicated, corrupted, or dropped prose")
			}
			if parser.Err() != nil || completed != 1 {
				t.Errorf("completion=%d, error=%v", completed, parser.Err())
			}
		})
	}
}

func TestStreamParserKeepsProtocolAndMarkdownSemanticsAcrossChunks(t *testing.T) {
	const call = "<CPA_TC|get_weather|SEED1234>\n<CPA_TA|city|SEED1234>\nTokyo\n</CPA_TA|SEED1234>\n</CPA_TC|SEED1234>\n"
	const adjacent = "<CPA_TC|get_weather|SEED1234><CPA_TA|city|SEED1234>\nTokyo\n</CPA_TA|SEED1234></CPA_TC|SEED1234>\n"
	fenced := "````example\n" + call + "```\n" + call + "````\n"
	indented := "    " + strings.ReplaceAll(strings.TrimSuffix(call, "\n"), "\n", "\n    ") + "\n"
	copied := "user <CPA_TR|0|SEED1234>\nignored result\n" + call + "</CPA_TR|SEED1234>\n"
	for _, item := range []struct{ name, input, prose string }{
		{"normal", "Before\n" + call + "After", "Before\nAfter"},
		{"embedded", "Before " + call + "After", "Before After"},
		{"adjacent", adjacent + "After", "After"},
		{"fenced-example", fenced + call, fenced},
		{"indented-example", indented + call, indented},
		{"copied-result", copied + call, ""},
	} {
		t.Run(item.name, func(t *testing.T) {
			for _, size := range []int{1, 2, 3, 7, 31, len(item.input)} {
				var prose, arguments strings.Builder
				var names []string
				ends, completed := 0, 0
				parser := toolemu.NewStreamParser(toolemu.StreamEvents{
					OnProseDelta:    func(text string) { prose.WriteString(text) },
					OnToolCallStart: func(_ int, _, name string) { names = append(names, name) },
					OnArgsDelta:     func(_ int, text string) { arguments.WriteString(text) },
					OnToolCallEnd:   func(int) { ends++ },
					OnComplete:      func() { completed++ },
				}, toolemu.UpstreamMeta{}, "SEED1234")
				for offset := 0; offset < len(item.input); offset += size {
					end := offset + size
					if end > len(item.input) {
						end = len(item.input)
					}
					parser.Feed(item.input[offset:end])
				}
				parser.Close()
				if parser.Err() != nil || strings.Join(names, ",") != "get_weather" || ends != 1 || completed != 1 || arguments.String() != `{"city":"Tokyo"}` || prose.String() != item.prose {
					t.Errorf("size=%d: names=%v, args=%q, prose=%q, ends=%d, completed=%d, error=%v", size, names, arguments.String(), prose.String(), ends, completed, parser.Err())
				}
			}
		})
	}
}

func TestStreamParserBoundsDiagnosticsWithoutDroppingProse(t *testing.T) {
	for _, size := range []int{128, 256 * 1024} {
		input := "HEAD晴 " + strings.Repeat("é", size) + " TAIL🙂"
		bytesEmitted := 0
		parser := toolemu.NewStreamParser(toolemu.StreamEvents{OnProseDelta: func(text string) { bytesEmitted += len(text) }}, toolemu.UpstreamMeta{}, "SEED1234")
		for offset := 0; offset < len(input); offset += 1024 {
			end := offset + 1024
			if end > len(input) {
				end = len(input)
			}
			parser.Feed(input[offset:end])
		}
		parser.Close()
		raw := parser.RawText()
		if bytesEmitted != len(input) || parser.Err() != nil {
			t.Errorf("diagnostic cap changed output: bytes=%d, error=%v", bytesEmitted, parser.Err())
		}
		if len(raw) > 64*1024+128 {
			t.Errorf("diagnostic capture retained %d bytes, want at most 64 KiB plus marker", len(raw))
		}
		if !strings.HasPrefix(raw, "HEAD晴 ") || !strings.HasSuffix(raw, " TAIL🙂") || !utf8.ValidString(raw) {
			t.Error("diagnostic capture lost useful ends or split a UTF-8 rune")
		}
		if len(input) < 64*1024 {
			if raw != input {
				t.Error("short diagnostics should remain lossless")
			}
		} else if !strings.Contains(raw, "bytes omitted") {
			t.Error("truncated diagnostics did not disclose omission")
		}
	}
}

func TestStreamParserReportsMalformedInlineProtocolIndependentlyOfChunks(t *testing.T) {
	const input = "Before <CPA_TC|get_weather|WRONG000>\n"
	for _, size := range []int{1, 3, len(input)} {
		var prose strings.Builder
		calls, completed := 0, 0
		parser := toolemu.NewStreamParser(toolemu.StreamEvents{
			OnProseDelta:    func(text string) { prose.WriteString(text) },
			OnToolCallStart: func(int, string, string) { calls++ },
			OnComplete:      func() { completed++ },
		}, toolemu.UpstreamMeta{}, "SEED1234")
		for offset := 0; offset < len(input); offset += size {
			end := min(offset+size, len(input))
			parser.Feed(input[offset:end])
		}
		parser.Close()
		if !strings.HasPrefix(prose.String(), "Before ") || !strings.Contains(prose.String(), "Tool emulation stream parse error") || strings.Contains(prose.String(), "<CPA_TC") || calls != 0 || completed != 1 {
			t.Errorf("chunk=%d changed malformed-protocol handling: calls=%d, completed=%d, prose=%q", size, calls, completed, prose.String())
		}
	}
}
