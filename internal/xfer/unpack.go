package xfer

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"

	"uc-pxx-installer/internal/zstd"
)

// unpacker unpacks a zstd stream while its bytes arrive (mainline-rc-10,
// owner R21: the compressed root file system of a schema-3 manifest). The
// receive loop gives it the compressed bytes (feed, in the loop's order);
// a goroutine decodes them and writes the unpacked bytes with out (the
// session's file), and hashes them. Only the unpacked file is kept: the
// compressed bytes are never stored, so the RAM holds one copy of the root
// file system, as for an uncompressed send.
//
// It never blocks the receive loop for good: after a decode error, a write
// error or more than limit unpacked bytes, the goroutine drains the rest of
// the stream, so the loop reads every byte of the PUT and stays in step
// with the link; the error comes back from finish.
type unpacker struct {
	pw    *io.PipeWriter
	done  chan struct{}
	limit int64
	n     int64
	h     [32]byte
	err   error
}

// errTooLarge: the stream unpacks to more than the manifest's size.
var errTooLarge = errors.New("the stream unpacks to more bytes than the manifest's rootfs size")

// limitWriter writes at most limit bytes to w and hashes them.
type limitWriter struct {
	w     func([]byte) error
	limit int64
	n     int64
	h     interface {
		io.Writer
		Sum([]byte) []byte
	}
}

func (l *limitWriter) Write(p []byte) (int, error) {
	if l.n+int64(len(p)) > l.limit {
		return 0, errTooLarge
	}
	if err := l.w(p); err != nil {
		return 0, err
	}
	l.h.Write(p)
	l.n += int64(len(p))
	return len(p), nil
}

// newUnpacker starts the decoder; out writes unpacked bytes (the session's
// open file), limit is the manifest's unpacked size.
func newUnpacker(out func([]byte) error, limit int64) *unpacker {
	pr, pw := io.Pipe()
	u := &unpacker{pw: pw, done: make(chan struct{}), limit: limit}
	go func() {
		defer close(u.done)
		lw := &limitWriter{w: out, limit: limit, h: sha256.New()}
		_, err := io.Copy(lw, zstd.NewReader(pr))
		// drain: the receive loop must be able to give every byte of the PUT
		io.Copy(io.Discard, pr)
		u.n = lw.n
		copy(u.h[:], lw.h.Sum(nil))
		u.err = err
	}()
	return u
}

// feed gives the decoder compressed bytes. It never returns an error: an
// error of the decoder is kept for finish (the goroutine drains).
func (u *unpacker) feed(p []byte) { u.pw.Write(p) }

// finish ends the stream and waits for the decoder: the unpacked size and
// sha256, or why the stream is not exactly limit unpacked bytes.
func (u *unpacker) finish() (int64, string, error) {
	u.pw.Close()
	<-u.done
	switch {
	case u.err != nil:
		return u.n, "", fmt.Errorf("unpack: %w", u.err)
	case u.n != u.limit:
		return u.n, "", fmt.Errorf("unpack: %d bytes, the manifest's rootfs has %d", u.n, u.limit)
	}
	return u.n, hex.EncodeToString(u.h[:]), nil
}

// abort ends the decoder after a link error or an idle end (nothing of it
// is used) and waits for it.
func (u *unpacker) abort() {
	u.pw.CloseWithError(errEnded)
	<-u.done
}
