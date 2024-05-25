package image

import "sync/atomic"
import "io"

type CounterWriter struct {
	io.Writer
	counter uint64
}

func NewCounterWriter(w io.Writer) *CounterWriter {
	return &CounterWriter{Writer: w}
}

func (w *CounterWriter) Write(b []byte) (int, error) {
	atomic.AddUint64(&w.counter, uint64(len(b)))
	return w.Writer.Write(b)
}

func (w *CounterWriter) Bytes() uint64 {
	return atomic.LoadUint64(&w.counter)
}
