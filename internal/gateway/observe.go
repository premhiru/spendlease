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

// eventSeparator terminates a server-sent event. Filtering has to work on
// whole events, not arbitrary read boundaries, so this is where blocks split.
var eventSeparator = []byte("\n\n")

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

	// drop decides whether an event should be withheld from the client.
	//
	// When nil — the overwhelming majority of responses — Read is a
	// byte-for-byte pass-through and this type only watches. Setting it turns
	// the reader into a filter, which has to hold each event until it is
	// complete before deciding. That costs nothing perceptible for
	// server-sent events, which are small and arrive whole, but it is a real
	// behavioural difference and is switched on only for requests spendlease
	// itself modified.
	drop func(payload []byte) bool

	streaming bool

	// out holds filtered bytes waiting to be handed to the caller.
	out bytes.Buffer
	// srcErr is the terminal error from the wrapped body, replayed to the
	// caller once everything kept has been delivered.
	srcErr error
	// scratch is the read buffer used in filtering mode.
	scratch []byte

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
	if o.drop != nil {
		return o.readFiltered(p)
	}

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

// readFiltered serves the caller from events that survived the drop
// predicate.
//
// Each complete event is forwarded the moment it is whole, so a stream still
// arrives incrementally; only the withheld chunk never appears.
func (o *observingReader) readFiltered(p []byte) (int, error) {
	for o.out.Len() == 0 {
		if o.srcErr != nil {
			if o.srcErr == io.EOF {
				o.sawEOF = true
				o.finish(true)
			}
			return 0, o.srcErr
		}

		if o.scratch == nil {
			o.scratch = make([]byte, 32*1024)
		}
		n, err := o.src.Read(o.scratch)
		if n > 0 {
			o.consumeEvents(o.scratch[:n])
		}
		if err != nil {
			o.srcErr = err
			// Whatever remains is not a complete event, but the client is
			// entitled to it: a truncated stream should look truncated, not
			// silently shortened.
			o.flushRemainder()
		}
	}

	n, _ := o.out.Read(p)
	return n, nil
}

// consumeEvents splits input on event boundaries, observing each one and
// keeping the ones that are not dropped.
func (o *observingReader) consumeEvents(b []byte) {
	o.pending.Write(b)

	for {
		buf := o.pending.Bytes()
		idx := bytes.Index(buf, eventSeparator)
		if idx < 0 {
			return
		}

		block := make([]byte, idx+len(eventSeparator))
		copy(block, buf[:idx+len(eventSeparator)])
		o.pending.Next(idx + len(eventSeparator))

		payload := eventPayload(block)
		if payload != nil {
			o.handleLine(append(append([]byte{}, ssePrefix...), payload...))
			if o.drop(payload) {
				continue
			}
		}
		o.out.Write(block)
	}
}

// flushRemainder emits whatever is left once the upstream ends.
func (o *observingReader) flushRemainder() {
	if o.pending.Len() == 0 {
		return
	}
	rest := o.pending.Bytes()
	if payload := eventPayload(rest); payload != nil {
		o.handleLine(append(append([]byte{}, ssePrefix...), payload...))
		if o.drop(payload) {
			o.pending.Reset()
			return
		}
	}
	o.out.Write(rest)
	o.pending.Reset()
}

// eventPayload returns the data payload of an event block, or nil when it
// carries none.
func eventPayload(block []byte) []byte {
	for _, line := range bytes.Split(block, []byte("\n")) {
		line = bytes.TrimRight(line, "\r")
		if !bytes.HasPrefix(line, ssePrefix) {
			continue
		}
		if p := bytes.TrimSpace(line[len(ssePrefix):]); len(p) > 0 {
			return p
		}
	}
	return nil
}

// Close releases the upstream body and settles the accounting.
//
// Reaching here without EOF means the client hung up mid-response. That is a
// normal event rather than an error, and the usage seen so far is still worth
// recording.
func (o *observingReader) Close() error {
	err := o.src.Close()
	o.finish(o.sawEOF || o.srcErr == io.EOF)
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
