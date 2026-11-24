package toolemu_test

import (
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/jianhongyu136/plugin-tool-emulation/internal/toolemu"
	log "github.com/sirupsen/logrus"
)

func FuzzStreamParserChunkBoundaries(f *testing.F) {
	for _, text := range []string{
		"plain prose 晴天🙂\n",
		"Before <CPA_TC|get_weather|WRONG000>\n",
		"<CPA_TC|get_weather|SEED1234>\n<CPA_TA|city|SEED1234>\nTokyo\n</CPA_TA|SEED1234>\n</CPA_TC|SEED1234>\n",
		"```\n<CPA_TC|example|SEED1234>\n```\n",
		"user <CPA_TR|0|SEED1234>\nignored\n</CPA_TR|SEED1234>\n",
	} {
		f.Add(text, uint8(3))
	}
	previous := log.StandardLogger().Out
	log.SetOutput(io.Discard)
	f.Cleanup(func() { log.SetOutput(previous) })
	f.Fuzz(func(t *testing.T, input string, chunk uint8) {
		if len(input) > 16*1024 {
			t.Skip()
		}
		observe := func(size int) string {
			var events strings.Builder
			var prose strings.Builder
			parser := toolemu.NewStreamParser(toolemu.StreamEvents{
				OnProseDelta:    func(text string) { prose.WriteString(text) },
				OnToolCallStart: func(index int, _, name string) { fmt.Fprintf(&events, "start:%d:%q;", index, name) },
				OnArgsDelta:     func(index int, text string) { fmt.Fprintf(&events, "args:%d:%q;", index, text) },
				OnToolCallEnd:   func(index int) { fmt.Fprintf(&events, "end:%d;", index) },
				OnComplete:      func() { events.WriteString("complete;") },
				OnError:         func(err error) { fmt.Fprintf(&events, "error:%v;", err) },
			}, toolemu.UpstreamMeta{}, "SEED1234")
			for offset := 0; offset < len(input); offset += size {
				parser.Feed(input[offset:min(offset+size, len(input))])
			}
			parser.Close()
			return fmt.Sprintf("%s|prose:%q|error:%v", events.String(), prose.String(), parser.Err())
		}
		whole := observe(max(1, len(input)))
		fragmented := observe(int(chunk) + 1)
		if whole != fragmented {
			t.Fatalf("chunk=%d changed output\nwhole=%s\nfragmented=%s", int(chunk)+1, whole, fragmented)
		}
	})
}
