// Copyright (C) 2023-2026 Òscar Casajuana Alonso

package http

import (
	"io"
	"net"
	stdhttp "net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// connStats records what a test server saw on the wire: how many connections
// were accepted and how many are still open. It's the only way to observe what
// the client does with keep-alive, since reading and closing every response
// body says nothing about whether the connection it arrived on was handed back
// to a pool that nobody ever closes.
type connStats struct {
	sync.Mutex
	accepted int
	open     int
}

func (s *connStats) snapshot() (accepted, open int) {
	s.Lock()
	defer s.Unlock()

	return s.accepted, s.open
}

// countingServer answers every request with status while tracking connections
func countingServer(status int) (*httptest.Server, *connStats) {
	stats := &connStats{}
	srv := httptest.NewUnstartedServer(stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		w.WriteHeader(status)
		_, _ = io.WriteString(w, "body")
	}))
	srv.Config.ConnState = func(_ net.Conn, state stdhttp.ConnState) {
		stats.Lock()
		defer stats.Unlock()
		switch state {
		case stdhttp.StateNew:
			stats.accepted++
			stats.open++
		case stdhttp.StateClosed, stdhttp.StateHijacked:
			stats.open--
		}
	}
	srv.Start()

	return srv, stats
}

// TestHTTPReusesConnections is the regression test for connections piling up
// on the router during bulk downloads: the client used to be built fresh for
// every request, and a Transport owns the pool it hands connections back to.
// Each per-request pool kept its own idle keep-alive connection ESTABLISHED
// until the process exited — the pool was unreachable but never collected,
// because its readLoop goroutine still referenced it.
func TestHTTPReusesConnections(t *testing.T) {
	srv, stats := countingServer(stdhttp.StatusOK)
	defer srv.Close()

	const requests = 12
	for i := 0; i < requests; i++ {
		body, err := Get(RequestParams{URL: srv.URL})
		if err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
		if _, err := io.ReadAll(body); err != nil {
			_ = body.Close()
			t.Fatalf("read %d: %v", i, err)
		}
		_ = body.Close()
	}

	accepted, open := stats.snapshot()

	// sequential requests to one host must ride a single keep-alive
	// connection: a higher count means each request built its own pool
	if accepted > 1 {
		t.Errorf("%d requests opened %d connections, want 1 (keep-alive is not being reused)", requests, accepted)
	}
	// and no connection may be left held open once the bodies are closed
	if open > 1 {
		t.Errorf("%d requests left %d connections open, want at most 1 (idle connections are leaking)", requests, open)
	}
}

// TestHTTPFailedRequestsReleaseConnections is the same leak through the other
// door: a non-200 response is returned as an error, and the body it arrived on
// was never closed, so every 404 — and retries multiply them — held a
// connection open for the rest of the run.
func TestHTTPFailedRequestsReleaseConnections(t *testing.T) {
	srv, stats := countingServer(stdhttp.StatusNotFound)
	defer srv.Close()

	const requests = 12
	for i := 0; i < requests; i++ {
		if _, err := Get(RequestParams{URL: srv.URL}); err == nil {
			t.Fatalf("request %d: want a 404 error, got nil", i)
		}
	}

	accepted, open := stats.snapshot()
	if accepted > 1 || open > 1 {
		t.Errorf("%d failed requests opened %d connections and left %d open, want 1 opened and at most 1 open", requests, accepted, open)
	}
}

// TestHTTPIdleConnectionsExpire is the user-visible half of the fix: an idle
// connection has to disappear on its own shortly after the work that opened
// it is done, instead of lingering in the pool until the process exits (which
// is what the router's connection list showed during bulk downloads). The
// production timeout is shortened here so the test waits milliseconds, not
// quarter-minutes.
func TestHTTPIdleConnectionsExpire(t *testing.T) {
	srv, stats := countingServer(stdhttp.StatusOK)
	defer srv.Close()

	original := transport.IdleConnTimeout
	transport.IdleConnTimeout = 100 * time.Millisecond
	defer func() { transport.IdleConnTimeout = original }()

	body, err := Get(RequestParams{URL: srv.URL})
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	_, _ = io.ReadAll(body)
	_ = body.Close()

	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, open := stats.snapshot(); open == 0 {
			return
		}
		if time.Now().After(deadline) {
			_, open := stats.snapshot()
			t.Fatalf("idle connection still open after IdleConnTimeout: %d", open)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
