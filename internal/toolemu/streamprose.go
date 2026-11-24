package toolemu

import (
	"bytes"
	"strings"
)

const proseFlushBytes = 4096

func streamProtocolPrefixes(proto protocolSettings) []string {
	return []string{
		"<" + proto.tags.Tool + "|", "<" + proto.tags.Arg + "|", "<" + proto.tags.Result + "|",
		"</" + proto.tags.Tool + "|", "</" + proto.tags.Arg + "|", "</" + proto.tags.Result + "|",
	}
}

func (p *StreamParser) feedRune(r rune) {
	p.line.WriteRune(r)
	if r == '\n' || p.line.Len() >= min(proseFlushBytes, p.maxBufferedBytes) {
		p.flushProseFragment()
	}
	if !p.checkBufferLimit() {
		return
	}
	if r == '\n' {
		p.processLine(p.line.String())
		p.resetLine()
	}
}

func (p *StreamParser) resetLine() {
	if p.line.Cap() > proseFlushBytes {
		p.line = bytes.Buffer{}
	} else {
		p.line.Reset()
	}
	p.lineEmitted = false
	p.lineLiteral = false
	p.lineProtocolHeld = false
}

// flushProseFragment emits only text that cannot become a protocol tag or a
// line-leading Markdown fence. Protocol candidates still use the line parser.
func (p *StreamParser) flushProseFragment() {
	if p.failed || p.lineProtocolHeld || p.state != streamStateProse && p.state != streamStateToolCalls || p.line.Len() == 0 {
		return
	}
	line := p.line.String()
	if !p.lineEmitted {
		if p.state == streamStateToolCalls && strings.TrimSpace(line) == "" {
			return
		}
		if p.markdownFence == 0 && isMarkdownIndentedCodeLine(line) {
			p.lineLiteral = true
		} else if p.holdsMarkdownFencePrefix(line) {
			// Once a held line fills a flush block, avoid rescanning it on
			// every rune; it will finish at newline or hit the buffer limit.
			p.lineProtocolHeld = p.line.Len() >= min(proseFlushBytes, p.maxBufferedBytes)
			return
		}
	}
	cut, complete := len(line), false
	if p.markdownFence == 0 && !p.lineLiteral {
		cut, complete = p.proseProtocolCut(line)
		if !p.lineEmitted {
			body := strings.TrimLeft(line, " \t")
			if strings.HasPrefix("user", body) || strings.TrimSpace(body) == "user" || cut < len(line) && isCopiedResultRolePrefix(line[:cut]) {
				p.lineProtocolHeld = complete || p.line.Len() >= min(proseFlushBytes, p.maxBufferedBytes)
				return
			}
		}
	}
	if cut == 0 {
		p.lineProtocolHeld = complete
		return
	}
	p.state = streamStateProse
	p.emitProse(line[:cut])
	p.line.Reset()
	p.line.WriteString(line[cut:])
	p.lineEmitted = true
	p.lineProtocolHeld = complete
}

func (p *StreamParser) holdsMarkdownFencePrefix(line string) bool {
	body, ok := markdownFenceBody(line)
	if !ok {
		return false
	}
	if body == "" {
		return true
	}
	marker := body[0]
	if marker != '`' && marker != '~' || p.markdownFence != 0 && marker != p.markdownFence {
		return false
	}
	count := countLeadingMarkdownFenceMarkers(body, marker)
	if count == len(body) {
		return true
	}
	if p.markdownFence == 0 {
		return count >= 3
	}
	return count >= p.markdownFenceLen && strings.TrimSpace(body[count:]) == ""
}

func (p *StreamParser) proseProtocolCut(line string) (int, bool) {
	for start := 0; start < len(line); {
		relative := strings.IndexByte(line[start:], '<')
		if relative < 0 {
			break
		}
		start += relative
		tail := line[start:]
		for _, prefix := range p.protocolPrefixes {
			if strings.HasPrefix(tail, prefix) {
				return start, true
			}
			if strings.HasPrefix(prefix, tail) {
				return start, false
			}
		}
		start++
	}
	return len(line), false
}
