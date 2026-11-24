package toolemu

import (
	"fmt"
	"strings"
)

const streamDiagnosticBytes = 64 * 1024

// DefaultMaxStreamBufferBytes bounds raw arguments and unfinished protocol lines.
const DefaultMaxStreamBufferBytes = 8 * 1024 * 1024

// StreamParserLimits controls raw protocol buffering, not forwarded prose or
// diagnostic retention. A non-positive value selects the safe default.
type StreamParserLimits struct {
	MaxBufferedBytes int
}

// StreamBufferLimitError reports an oversized unfinished tool call or line.
type StreamBufferLimitError struct {
	Limit int
}

func (e StreamBufferLimitError) Error() string {
	return fmt.Sprintf("toolemu stream: buffer limit exceeded (%d bytes)", e.Limit)
}

func (p *StreamParser) checkBufferLimit() bool {
	buffered := p.line.Len() + p.argBuf.Len() + p.argsBytes + len(p.argName) + len(p.toolName) + len(p.pendingBytes)
	if buffered > p.maxBufferedBytes {
		p.Abort(StreamBufferLimitError{Limit: p.maxBufferedBytes})
		return false
	}
	return true
}

// streamDiagnosticText keeps a prefix and a circular tail. Appending remains
// linear in the received bytes; it never copies the retained tail per chunk.
type streamDiagnosticText struct {
	prefix []byte
	tail   []byte
	next   int
	total  int64
}

func (d *streamDiagnosticText) WriteString(text string) {
	d.total += int64(len(text))
	const side = streamDiagnosticBytes / 2
	if len(d.prefix) < side {
		count := min(side-len(d.prefix), len(text))
		d.prefix = append(d.prefix, text[:count]...)
		text = text[count:]
	}
	if len(text) == 0 {
		return
	}
	if len(text) >= side {
		d.tail = append(d.tail[:0], text[len(text)-side:]...)
		d.next = 0
		return
	}
	if len(d.tail) < side {
		count := min(side-len(d.tail), len(text))
		d.tail = append(d.tail, text[:count]...)
		text = text[count:]
	}
	if len(text) == 0 {
		return
	}
	count := min(side-d.next, len(text))
	copy(d.tail[d.next:], text[:count])
	copy(d.tail, text[count:])
	d.next = (d.next + len(text)) % side
}

func (d *streamDiagnosticText) String() string {
	prefix := string(d.prefix)
	tail := string(d.tail)
	if d.next > 0 {
		tail = string(d.tail[d.next:]) + string(d.tail[:d.next])
	}
	if d.total <= int64(len(prefix)+len(tail)) {
		return prefix + tail
	}
	// Retention boundaries may split a rune; do not introduce invalid UTF-8 in
	// a truncated diagnostic or silently expand the configured byte budget.
	prefix = strings.ToValidUTF8(prefix, "")
	tail = strings.ToValidUTF8(tail, "")
	omitted := d.total - int64(len(prefix)+len(tail))
	return prefix + fmt.Sprintf("\n[... %d bytes omitted ...]\n", omitted) + tail
}
