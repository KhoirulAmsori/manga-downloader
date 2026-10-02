// Copyright (C) 2023-2026 Òscar Casajuana Alonso

package http

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Params is an interface for request parameters
type Params interface {
	GetURL() string
	GetReferer() string
}

// RequestParams is a struct for base request parameters
type RequestParams struct {
	URL     string
	Referer string
	Origin  string
	// Headers are extra headers sent with the request (e.g. a CSRF token
	// read off a previously fetched page)
	Headers map[string]string
	// Form, if set, is sent as an application/x-www-form-urlencoded POST
	// body instead of an empty one
	Form url.Values
	// Body is an optional url-encoded form body; when set, it's sent as
	// application/x-www-form-urlencoded (used by wp-admin/admin-ajax.php
	// style endpoints, i.e. utoon.us)
	Body string
}

// GetURL returns the request URL
func (r RequestParams) GetURL() string {
	return r.URL
}

// GetReferer returns the request referer
func (r RequestParams) GetReferer() string {
	return r.Referer
}

// userAgent is sent with every request: some sites block Go's default
// "Go-http-client" user agent with a 403/500
const userAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0 Safari/537.36"

// transport is the connection pool every request goes through.
//
// A Transport owns the keep-alive connections its responses are handed back
// to, so it must not be built per request: a fresh pool per request means no
// reuse at all (a new TCP+TLS connection every time), and the pool each
// request made is then orphaned while still holding its connection
// ESTABLISHED — the connection's readLoop goroutine keeps the pool reachable,
// so the GC never gets the chance to close it. A bulk download is thousands
// of requests, so connections piled up for the whole run and only went away
// when the process exited.
//
// The knobs that matter for keeping the count visible on a router sane:
//
//   - ForceAttemptHTTP2: setting DialContext alone turns HTTP/2 off (Go
//     conservatively declines it once a custom dialer is present), and h2 is
//     the biggest single win here — every stream to a host shares one
//     connection, so a 50-way page download holds one connection per host
//     instead of one per stream. The per-request transports this replaced got
//     h2 by accident (no dialer set) and then threw the negotiated
//     connection away with the pool it came in.
//   - IdleConnTimeout is what empties the pool once a chapter's last page is
//     done: zero would keep an idle connection forever, and the default 90s
//     outlives the burst of requests it was pooled for, leaving it waiting on
//     the server to close first.
//   - MaxIdleConnsPerHost is the pool size per host (2 by default, raised to
//     the page-concurrency ceiling so --concurrency-pages doesn't churn
//     handshakes).
//   - Proxy is nil, as it was before this shared the pool: honouring
//     HTTP_PROXY/HTTPS_PROXY is a separate decision that would silently
//     reroute downloads for anyone who has those set.
var transport = &http.Transport{
	// keep the original behaviour: never transparently gunzip a response body
	DisableCompression:  true,
	DialContext:         (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
	TLSHandshakeTimeout: 10 * time.Second,
	IdleConnTimeout:     15 * time.Second,
	MaxIdleConns:        100,
	MaxIdleConnsPerHost: 10,
	// see the note above: without this the DialContext above switches HTTP/2
	// off and every stream becomes its own connection again
	ForceAttemptHTTP2: true,
}

// client is the one HTTP client every request goes through, sharing
// transport's connection pool
var client = &http.Client{Transport: transport}

// request sends a request to the given URL
func request(t string, params Params) (body io.ReadCloser, err error) {
	rp, _ := params.(RequestParams)

	var reqBody io.Reader
	if rp.Form != nil {
		reqBody = strings.NewReader(rp.Form.Encode())
	} else if rp.Body != "" {
		reqBody = strings.NewReader(rp.Body)
	}

	req, _ := http.NewRequest(t, params.GetURL(), reqBody)
	req.Header.Set("User-Agent", sessionUserAgent(userAgent))
	if cookies := sessionCookies(req.URL.Hostname()); cookies != "" {
		req.Header.Set("Cookie", cookies)
	}
	// some WAFs (e.g. ddos-guard) reject requests missing these browser headers
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,*/*;q=0.8")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	if ref := params.GetReferer(); ref != "" {
		// browsers always send at least the root path in the referer; some
		// image cdns reject referers without it
		if u, err := url.Parse(ref); err == nil && u.Path == "" {
			ref += "/"
		}
		req.Header.Add("Referer", ref)
	}
	if rp.Origin != "" {
		req.Header.Set("Origin", rp.Origin)
	}
	if rp.Form != nil || rp.Body != "" {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	for k, v := range rp.Headers {
		req.Header.Set(k, v)
	}

	resp, err := client.Do(req)
	if err != nil {
		// Do can return a response alongside the error (a redirect policy
		// that gives up, for one): its body still has to go, or the
		// connection underneath it never returns to the pool
		if resp != nil && resp.Body != nil {
			resp.Body.Close()
		}
		return
	}

	// harvest any session cookies the server sets (e.g. a PHPSESSID tied to
	// a CSRF token read off the page), so subsequent requests to the same
	// site stay authenticated without a manual cookie jar
	for _, c := range resp.Cookies() {
		domain := strings.TrimPrefix(c.Domain, ".")
		if domain == "" {
			domain = req.URL.Hostname()
		}
		SetCookie(domain, c.Name, c.Value)
	}

	if resp.StatusCode != 200 {
		// the body must be closed here: on the error path nobody below ever
		// gets to close it, and an unread body keeps its connection checked
		// out of the pool for the lifetime of the process. Retries multiply
		// failed requests, so this path leaked one connection per attempt;
		// draining it also lets Go reuse the connection instead of tearing
		// it down.
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		err = fmt.Errorf("received %d response code", resp.StatusCode)
		return
	}

	body = resp.Body
	return
}
