package gateway

import (
	"bytes"
	"io"
	"sync"
)

// maxObservedBody caps how much of a non-streaming response is held in order
// to read its usage object.
//
// Usage sits at the top level of a JSON response, so the whole body has to be
// available to parse it. A cap is still needed: an embeddings response can be
// megabytes of vectors, and holding an unbounded copy of every response would
// turn a proxy into a memory leak. Past the cap the body still reaches the
// client untouched; only the accounting falls back to an estimate.
const maxObservedBody = 1 << 20 // 1 MiB

// ssePrefix is the field that carries an event's payload.
var ssePrefix = []byte("data:")

// observingReader wraps a response body, passing every byte through unchanged
// while reading usage out of the stream as it goes.
//
// This is the shape the whole phase depends on. Buffering the response to
// account for it would destroy streaming, which is the property phase 3
// established and tested. Instead the bytes are forwarded immediately and
// observed on the way past.
type observingReader struct {
	src io.ReadCloser

	// onEvent receives each server-sent event payload, for streamed responses.
	onEvent func(payload []byte)
	// onBody receives the accumulated body, for non-streamed responses.
	onBody func(body []byte)
	// onDone runs exactly once, when the body is exhausted or closed. A
	// close before EOF is a client disconnect, which is reported as such.
	onDone func(complete bool)

	streaming bool

	// pending holds a partial line between reads, because a Read boundary
	// can fall anywhere, including inside an event.
	pending bytes.Buffer
	// body accumulates a non-streaming response, up to maxObservedBody.
	body bytes.Buffer
	// truncated records that the body outgrew the cap.
	truncated bool

	once     sync.Once
	sawEOF   bool
	overflow bool
}

// newObservingReader wraps src.
func newObservingReader(src io.ReadCloser, streaming bool) *observingReader {
	return &observingReader{src: src, streaming: streaming}
}

// Read forwards from the wrapped body and observes what passes.
//
// The caller's buffer is filled exactly as the upstream produced it; the
// observation works on a copy of the same bytes.
func (o *observingReader) Read(p []byte) (int, error) {
	n, err := o.src.Read(p)
	if n > 0 {
		o.observe(p[:n])
	}
	if err == io.EOF {
		o.flush()
		o.sawEOF = true
		o.finish(true)
	}
	return n, err
}

// Close releases the upstream body and settles the accounting.
//
// Reaching here without EOF means the client hung up mid-response. That is a
// normal event rather than an error, and the usage seen so far is still worth
// recording.
func (o *observingReader) Close() error {
	err := o.src.Close()
	o.finish(o.sawEOF)
	return err
}

// finish runs the completion callback once.
func (o *observingReader) finish(complete bool) {
	o.once.Do(func() {
		if o.onDone != nil {
			o.onDone(complete)
		}
	})
}

// observe feeds bytes to the appropriate parser.
func (o *observingReader) observe(b []byte) {
	if !o.streaming {
		o.accumulate(b)
		return
	}
	o.scanEvents(b)
}

// accumulate collects a non-streaming body up to the cap.
func (o *observingReader) accumulate(b []byte) {
	if o.overflow {
		return
	}
	if o.body.Len()+len(b) > maxObservedBody {
		o.overflow = true
		o.truncated = true
		o.body.Reset()
		return
	}
	o.body.Write(b)
}

// scanEvents extracts complete server-sent event payloads from a chunk.
//
// Events are newline delimited, and a chunk boundary can fall anywhere, so
// the tail of an incomplete line is carried into the next call.
func (o *observingReader) scanEvents(b []byte) {
	// Guard against a malformed stream with no newlines growing without
	// bound. A single SSE event is never legitimately this large.
	if o.pending.Len() > maxObservedBody {
		o.pending.Reset()
		o.truncated = true
	}
	o.pending.Write(b)

	for {
		buf := o.pending.Bytes()
		idx := bytes.IndexByte(buf, '\n')
		if idx < 0 {
			return
		}

		line := bytes.TrimRight(buf[:idx], "\r")
		// Consume the line and its newline.
		o.pending.Next(idx + 1)

		o.handleLine(line)
	}
}

// handleLine reports a single event payload, if the line carries one.
func (o *observingReader) handleLine(line []byte) {
	if !bytes.HasPrefix(line, ssePrefix) {
		return
	}
	payload := bytes.TrimSpace(line[len(ssePrefix):])
	if len(payload) == 0 || bytes.Equal(payload, []byte("[DONE]")) {
		return
	}
	if o.onEvent != nil {
		o.onEvent(payload)
	}
}

// flush delivers whatever remains once the stream ends.
func (o *observingReader) flush() {
	if o.streaming {
		if line := bytes.TrimRight(o.pending.Bytes(), "\r\n"); len(line) > 0 {
			o.handleLine(line)
		}
		o.pending.Reset()
		return
	}
	if o.onBody != nil && !o.truncated {
		o.onBody(o.body.Bytes())
	}
}
