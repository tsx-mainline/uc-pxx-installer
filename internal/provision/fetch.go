package provision

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"sync/atomic"
	"time"
)

// Fetcher gets the bundle file over the network.
type Fetcher interface {
	// Get starts a GET of url, from offset off when off > 0 (an HTTP Range
	// "bytes=off-"). It gives the body and the offset the body starts at: off
	// for a partial answer (206 with exactly that range of a file of size
	// bytes), 0 when the server sent the whole file (200). Anything else is an
	// error.
	Get(ctx context.Context, url string, off, size int64) (io.ReadCloser, int64, error)
}

// The network bounds of the download.
const (
	DialTimeout   = 30 * time.Second
	HeaderTimeout = 60 * time.Second
	// StallTimeout ends a download that got no byte for this long (a try).
	StallTimeout = 60 * time.Second
	MaxRedirects = 10
)

// HTTPFetcher is the Fetcher of a unit: Go's HTTP client with the system's
// CA certificates, no proxy, no transparent decompression (the bytes are
// hashed as they arrive), at most MaxRedirects redirects, http and https
// only (GitHub's release assets redirect to their storage). The pins make
// every host equal: a host can only fail a try, not change what is
// installed.
func HTTPFetcher() Fetcher {
	tr := &http.Transport{
		Proxy:                 nil,
		DialContext:           (&net.Dialer{Timeout: DialTimeout, KeepAlive: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout:   DialTimeout,
		ResponseHeaderTimeout: HeaderTimeout,
		DisableCompression:    true,
		MaxIdleConns:          2,
		IdleConnTimeout:       30 * time.Second,
		ForceAttemptHTTP2:     true,
	}
	c := &http.Client{Transport: tr, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= MaxRedirects {
			return fmt.Errorf("more than %d redirects", MaxRedirects)
		}
		if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
			return fmt.Errorf("a redirect to %q", req.URL.Scheme)
		}
		return nil
	}}
	return &httpFetcher{c: c}
}

type httpFetcher struct{ c *http.Client }

var contentRange = regexp.MustCompile(`^bytes ([0-9]+)-([0-9]+)/([0-9]+|\*)$`)

func (h *httpFetcher) Get(ctx context.Context, url string, off, size int64) (io.ReadCloser, int64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("User-Agent", "uc-pxx-provision")
	req.Header.Set("Accept-Encoding", "identity")
	if off > 0 {
		req.Header.Set("Range", "bytes="+strconv.FormatInt(off, 10)+"-")
	}
	resp, err := h.c.Do(req)
	if err != nil {
		return nil, 0, err
	}
	switch {
	case resp.StatusCode == http.StatusOK:
		if resp.ContentLength >= 0 && resp.ContentLength != size {
			resp.Body.Close()
			return nil, 0, fmt.Errorf("HTTP 200 with %d bytes, the pin is %d", resp.ContentLength, size)
		}
		return resp.Body, 0, nil
	case resp.StatusCode == http.StatusPartialContent && off > 0:
		m := contentRange.FindStringSubmatch(resp.Header.Get("Content-Range"))
		a, _ := strconv.ParseInt(m1(m, 1), 10, 64)
		b, _ := strconv.ParseInt(m1(m, 2), 10, 64)
		if m == nil || a != off || b != size-1 || m[3] != "*" && m[3] != strconv.FormatInt(size, 10) ||
			resp.ContentLength >= 0 && resp.ContentLength != size-off {
			resp.Body.Close()
			return nil, 0, fmt.Errorf("HTTP 206 with Content-Range %q, not bytes %d-%d/%d", resp.Header.Get("Content-Range"), off, size-1, size)
		}
		return resp.Body, off, nil
	}
	resp.Body.Close()
	return nil, 0, fmt.Errorf("HTTP %s", resp.Status)
}

func m1(m []string, i int) string {
	if m == nil {
		return ""
	}
	return m[i]
}

// errStall: no byte for StallTimeout.
var errStall = errors.New("no byte arrived for 60 s")

// stallReader cancels the request when a Read gets nothing for the timeout.
type stallReader struct {
	r      io.Reader
	kick   chan struct{}
	cancel context.CancelFunc
	stall  *atomic.Bool
}

func watch(r io.Reader, cancel context.CancelFunc, d time.Duration) (*stallReader, func()) {
	s := &stallReader{r: r, kick: make(chan struct{}, 1), cancel: cancel, stall: new(atomic.Bool)}
	done := make(chan struct{})
	go func() {
		t := time.NewTimer(d)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-s.kick:
				if !t.Stop() {
					<-t.C
				}
				t.Reset(d)
			case <-t.C:
				s.stall.Store(true)
				cancel()
				return
			}
		}
	}()
	return s, func() { close(done) }
}

func (s *stallReader) Read(p []byte) (int, error) {
	n, err := s.r.Read(p)
	if n > 0 {
		select {
		case s.kick <- struct{}{}:
		default:
		}
	}
	if err != nil && s.stall.Load() {
		err = errStall
	}
	return n, err
}
