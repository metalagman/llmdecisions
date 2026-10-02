package main

import (
	"bytes"
	"crypto/tls"
	"errors"
	"io"
	"net/http"
	"net/http/httptrace"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestIntervalsAndUnavailable(t *testing.T) {
	start := time.Unix(100, 0)
	if got := elapsed(start, start.Add(1234*time.Microsecond)); got != "1.234" {
		t.Fatal(got)
	}
	for _, pair := range [][2]time.Time{{{}, start}, {start, {}}, {start, start.Add(-time.Second)}} {
		if got := elapsed(pair[0], pair[1]); got != "unavailable" {
			t.Fatal(got)
		}
	}
	r := newTimings(start)
	r.mark("wrote_request", start.Add(5*time.Millisecond))
	r.mark("first_byte", start.Add(4*time.Millisecond))
	r.mark("finish", start.Add(20*time.Millisecond))
	r.begin("tcp_connect", "tcp [::1]:443", start.Add(time.Millisecond))
	r.begin("tcp_connect", "tcp 127.0.0.1:443", start.Add(2*time.Millisecond))
	r.end("tcp_connect", "tcp 127.0.0.1:443", start.Add(4*time.Millisecond), nil)
	r.end("tcp_connect", "tcp [::1]:443", start.Add(3*time.Millisecond), errors.New("failed"))
	var b bytes.Buffer
	if err := r.report(&b, ""); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"ttfb 4.000", "post_write_first_byte_wait unavailable", "dns unavailable", "tls unavailable", "ttft_first_text unavailable", "tcp_connect[1]_ms = 2.000", "failed = true", "tcp_connect[2]_ms = 2.000", "total 20.000"} {
		if !strings.Contains(strings.Join(strings.Fields(b.String()), " "), want) {
			t.Errorf("missing %q in report:\n%s", want, b.String())
		}
	}
}

func TestProviderMilliseconds(t *testing.T) {
	for _, raw := range []string{"", "invalid", "NaN", "+Inf", "-1"} {
		if got := providerMilliseconds(raw); got != "unavailable" {
			t.Errorf("%q: %s", raw, got)
		}
	}
	if got := providerMilliseconds(" 1.25 "); got != "1.250" {
		t.Fatal(got)
	}
	if got := providerMilliseconds("0"); got != "0.000" {
		t.Fatal(got)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestTransportBodyAndHeaderAllowlist(t *testing.T) {
	r := newTimings(time.Now())
	base := roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{Proto: "HTTP/2.0", Header: http.Header{"Openai-Processing-Ms": {"12.5"}, "X-Request-Id": {"secret-key"}, "Server-Timing": {"edge;dur=2"}, "Authorization": {"secret-key"}}, Body: io.NopCloser(strings.NewReader("abc"))}, nil
	})
	tr := &timedTransport{base: base, timing: r}
	req, _ := http.NewRequest("GET", "https://example.test", nil)
	res, err := tr.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadAll(res.Body); err != nil {
		t.Fatal(err)
	}
	if err := res.Body.Close(); err != nil {
		t.Fatal(err)
	}
	if r.bytes != 3 || r.points["body_eof"].IsZero() || r.points["body_closed"].IsZero() {
		t.Fatal("body not measured")
	}
	var b bytes.Buffer
	if err := r.report(&b, "secret-key"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(b.String(), "secret-key") || strings.Contains(b.String(), "Authorization") {
		t.Fatal("secret/header leak")
	}
	if !strings.Contains(b.String(), "provider_processing_ms         12.500") {
		t.Fatal(b.String())
	}
}

func TestTraceConcurrentAndReused(t *testing.T) {
	r := newTimings(time.Now())
	tr := r.trace()
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Go(func() {
			tr.DNSStart(httptrace.DNSStartInfo{})
			tr.DNSDone(httptrace.DNSDoneInfo{})
			tr.ConnectStart("tcp", "127.0.0.1:443")
			tr.ConnectDone("tcp", "127.0.0.1:443", nil)
			tr.TLSHandshakeStart()
			tr.TLSHandshakeDone(tls.ConnectionState{}, nil)
			tr.GetConn("example.test")
			tr.GotConn(httptrace.GotConnInfo{Reused: true, WasIdle: true, IdleTime: time.Second})
			tr.WroteHeaders()
			tr.WroteRequest(httptrace.WroteRequestInfo{})
			tr.GotFirstResponseByte()
			var b bytes.Buffer
			if err := r.report(&b, ""); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if len(r.phases) != 48 {
		t.Fatalf("phases=%d", len(r.phases))
	}
	for _, p := range r.phases {
		if p.end.IsZero() || p.end.Before(p.start) {
			t.Fatal("unmatched interval")
		}
	}
	if r.metadata["connection_reused"] != "true" {
		t.Fatal("missing reuse")
	}
}

func TestTextDeltaStats(t *testing.T) {
	start := time.Unix(100, 0)
	r := newTimings(start)
	r.text(start.Add(10 * time.Millisecond))
	r.text(start.Add(12 * time.Millisecond))
	r.text(start.Add(18 * time.Millisecond))
	var b bytes.Buffer
	if err := r.report(&b, ""); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.String(), "2.000 / 4.000 / 6.000 / 2.000 / 6.000 (n=2)") {
		t.Fatal(b.String())
	}
}
