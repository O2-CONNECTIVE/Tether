package agentexec

// recordOverhead charges each retained record against MaxRetained so that many
// tiny interleaved stdout/stderr writes cannot grow slice metadata unbounded.
const recordOverhead = 64
const mergeLimit = 32 << 10

type record struct {
	stream Stream
	offset uint64
	data   []byte
}

// outputBuffer keeps one ordered stdout/stderr sequence. Offsets are assigned
// only to accepted bytes; bytes beyond MaxTotalOutput are counted as dropped.
// Every method requires Session.mu.
type outputBuffer struct {
	records   []record
	start     uint64 // first retained offset
	end       uint64 // next offset to assign
	retained  int
	dropped   uint64
	truncated bool
	released  bool // a later Start discarded unacknowledged data
	closed    bool // no further bytes will be assigned
	space     chan struct{}
}

func newOutputBuffer() *outputBuffer { return &outputBuffer{space: make(chan struct{})} }

func (b *outputBuffer) signalLocked() {
	close(b.space)
	b.space = make(chan struct{})
}

// appendLocked stores what fits and returns the rest when the retained window is
// full. The caller waits for space or its command context; it never drops data
// for backpressure, only past the total byte cap.
func (b *outputBuffer) appendLocked(l Limits, s Stream, data []byte) []byte {
	for len(data) > 0 {
		if b.closed || b.released {
			b.dropped += uint64(len(data))
			b.truncated = true
			return nil
		}
		if b.end >= l.MaxTotalOutput {
			b.dropped += uint64(len(data))
			b.truncated = true
			return nil
		}
		var last *record
		if n := len(b.records); n > 0 && b.records[n-1].stream == s && len(b.records[n-1].data) < mergeLimit {
			last = &b.records[n-1]
		}
		overhead, limit := recordOverhead, mergeLimit
		if last != nil {
			overhead, limit = 0, mergeLimit-len(last.data)
		}
		room := l.MaxRetained - b.retained - overhead
		if room <= 0 {
			return data
		}
		n := min(room, limit, len(data))
		if rest := l.MaxTotalOutput - b.end; uint64(n) > rest {
			n = int(rest)
		}
		if last != nil {
			last.data = append(last.data, data[:n]...)
		} else {
			b.records = append(b.records, record{stream: s, offset: b.end, data: append([]byte(nil), data[:n]...)})
		}
		b.retained += n + overhead
		b.end += uint64(n)
		data = data[n:]
	}
	return nil
}

// ackLocked frees retained bytes before cursor and wakes a backpressured pump.
func (b *outputBuffer) ackLocked(cursor uint64) {
	if cursor <= b.start {
		return
	}
	kept := b.records[:0]
	for _, r := range b.records {
		end := r.offset + uint64(len(r.data))
		switch {
		case end <= cursor:
			b.retained -= len(r.data) + recordOverhead
		case r.offset < cursor:
			skip := int(cursor - r.offset)
			b.retained -= skip
			r.data = append([]byte(nil), r.data[skip:]...)
			r.offset = cursor
			kept = append(kept, r)
		default:
			kept = append(kept, r)
		}
	}
	clear(b.records[len(kept):])
	b.records = kept
	b.start = cursor
	b.signalLocked()
}

// releaseLocked discards unacknowledged data of a finished command. Later
// readers observe the released range as a gap, never as silently missing data.
func (b *outputBuffer) releaseLocked() {
	if b.released {
		return
	}
	b.released, b.records, b.retained, b.start = true, nil, 0, b.end
	b.signalLocked()
}

func (b *outputBuffer) closeLocked() {
	if !b.closed {
		b.closed = true
		b.signalLocked()
	}
}

func (b *outputBuffer) readLocked(cursor uint64, max int) (Output, error) {
	if cursor > b.end {
		return Output{}, ErrCursor
	}
	b.ackLocked(cursor)
	out := Output{Next: cursor, Truncated: b.truncated, Dropped: b.dropped}
	if cursor < b.start {
		out.Gap, out.Next = b.start-cursor, b.start
	}
	for _, r := range b.records {
		if max <= 0 {
			break
		}
		end := r.offset + uint64(len(r.data))
		if end <= out.Next {
			continue
		}
		data := r.data[out.Next-r.offset:]
		if len(data) > max {
			data = data[:max]
		}
		out.Chunks = append(out.Chunks, Chunk{Stream: r.stream, Offset: out.Next, Data: append([]byte(nil), data...)})
		out.Next += uint64(len(data))
		max -= len(data)
	}
	out.Complete = (b.closed || b.released) && out.Next == b.end
	return out, nil
}
