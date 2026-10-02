package main

import (
	"crypto/tls"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptrace"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type phase struct {
	kind, address string
	start, end    time.Time
	failed        bool
}

type tokenUsage struct {
	input, output                 int64
	cached, cacheWrite, reasoning *int64
}

// Trace callbacks can run concurrently, including after an HTTP call returns.
type timings struct {
	mu       sync.Mutex
	start    time.Time
	points   map[string]time.Time
	phases   []phase
	metadata map[string]string
	bytes    int64
	deltas   int
	gaps     []time.Duration
	usage    *tokenUsage
}

func newTimings(start time.Time) *timings {
	return &timings{start: start, points: make(map[string]time.Time), metadata: make(map[string]string)}
}

func (t *timings) mark(name string, now time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.points[name].IsZero() {
		t.points[name] = now
	}
}

func (t *timings) begin(kind, address string, now time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if now.IsZero() {
		now = time.Now()
	}
	t.phases = append(t.phases, phase{kind: kind, address: address, start: now})
}

func (t *timings) end(kind, address string, now time.Time, err error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if now.IsZero() {
		now = time.Now()
	}
	for i := range t.phases {
		p := &t.phases[i]
		if p.kind == kind && p.address == address && p.end.IsZero() {
			p.end, p.failed = now, err != nil
			return
		}
	}
}

func (t *timings) trace() *httptrace.ClientTrace {
	return &httptrace.ClientTrace{
		// Capture paired timestamps while holding the same lock so concurrent
		// attempts with the same address retain chronological pairing.
		DNSStart:          func(httptrace.DNSStartInfo) { t.begin("dns", "", time.Time{}) },
		DNSDone:           func(info httptrace.DNSDoneInfo) { t.end("dns", "", time.Time{}, info.Err) },
		ConnectStart:      func(network, addr string) { t.begin("tcp_connect", network+" "+addr, time.Time{}) },
		ConnectDone:       func(network, addr string, err error) { t.end("tcp_connect", network+" "+addr, time.Time{}, err) },
		TLSHandshakeStart: func() { t.begin("tls", "", time.Time{}) },
		TLSHandshakeDone:  func(_ tls.ConnectionState, err error) { t.end("tls", "", time.Time{}, err) },
		GetConn:           func(string) { t.mark("get_conn", time.Now()) },
		GotConn: func(info httptrace.GotConnInfo) {
			t.mu.Lock()
			defer t.mu.Unlock()
			if t.points["got_conn"].IsZero() {
				t.points["got_conn"] = time.Now()
			}
			t.metadata["connection_reused"] = strconv.FormatBool(info.Reused)
			t.metadata["connection_was_idle"] = strconv.FormatBool(info.WasIdle)
			if info.WasIdle {
				t.metadata["connection_idle_ms"] = fmt.Sprintf("%.3f", float64(info.IdleTime)/float64(time.Millisecond))
			}
			if info.Conn != nil {
				t.metadata["peer"] = info.Conn.RemoteAddr().String()
			}
		},
		WroteHeaders: func() { t.mark("wrote_headers", time.Now()) },
		WroteRequest: func(info httptrace.WroteRequestInfo) {
			t.mark("write_finished", time.Now())
			if info.Err == nil {
				t.mark("wrote_request", time.Now())
			}
		},
		GotFirstResponseByte: func() { t.mark("first_byte", time.Now()) },
	}
}

type timedTransport struct {
	base   http.RoundTripper
	timing *timings
}

func (t *timedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	t.timing.mark("roundtrip_start", time.Now())
	if transport, ok := t.base.(*http.Transport); ok && transport.Proxy != nil {
		proxy, _ := transport.Proxy(req)
		t.timing.mu.Lock()
		t.timing.metadata["proxy_selected"] = strconv.FormatBool(proxy != nil)
		t.timing.mu.Unlock()
	}
	res, err := t.base.RoundTrip(req)
	if res != nil {
		t.timing.mark("headers_received", time.Now())
		t.timing.mu.Lock()
		t.timing.metadata["http_protocol"] = res.Proto
		for _, name := range []string{"X-Request-ID", "OpenAI-Processing-Ms", "Server-Timing"} {
			if value := res.Header.Get(name); value != "" {
				t.timing.metadata[strings.ToLower(name)] = value
			}
		}
		t.timing.mu.Unlock()
		if res.Body != nil {
			res.Body = &timedBody{ReadCloser: res.Body, timing: t.timing}
		}
	}
	return res, err
}

type timedBody struct {
	io.ReadCloser
	timing *timings
}

func (b *timedBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	now := time.Now()
	b.timing.mu.Lock()
	b.timing.bytes += int64(n)
	if n > 0 {
		if b.timing.points["first_body_read"].IsZero() {
			b.timing.points["first_body_read"] = now
		}
		b.timing.points["last_body_read"] = now
	}
	b.timing.mu.Unlock()
	if err == io.EOF {
		b.timing.mark("body_eof", now)
	}
	return n, err
}

func (b *timedBody) Close() error {
	err := b.ReadCloser.Close()
	b.timing.mark("body_closed", time.Now())
	return err
}

func elapsed(start, end time.Time) string {
	if start.IsZero() || end.IsZero() || end.Before(start) {
		return "unavailable"
	}
	return fmt.Sprintf("%.3f", float64(end.Sub(start))/float64(time.Millisecond))
}

func providerMilliseconds(raw string) string {
	n, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil || math.IsNaN(n) || math.IsInf(n, 0) || n < 0 {
		return "unavailable"
	}
	return fmt.Sprintf("%.3f", n)
}

func (t *timings) text(now time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.deltas == 0 {
		t.points["first_text"] = now
	} else {
		t.gaps = append(t.gaps, now.Sub(t.points["last_text"]))
	}
	t.points["last_text"] = now
	t.deltas++
}

var metricDefinitions = []struct{ name, from, to string }{
	{"sdk_to_transport", "start", "roundtrip_start"},
	{"connection_acquisition", "get_conn", "got_conn"},
	{"request_write", "got_conn", "wrote_request"},
	{"headers_write_at", "start", "wrote_headers"},
	{"request_write_finished_at", "start", "write_finished"},
	{"ttfb", "start", "first_byte"},
	{"post_write_first_byte_wait", "wrote_request", "first_byte"},
	{"response_headers_at", "start", "headers_received"},
	{"first_body_read_at", "start", "first_body_read"},
	{"last_body_read_at", "start", "last_body_read"},
	{"body_eof_at", "start", "body_eof"},
	{"body_closed_at", "start", "body_closed"},
	{"first_stream_event", "start", "first_event"},
	{"ttft_first_text", "start", "first_text"},
	{"last_text_at", "start", "last_text"},
	{"text_span", "first_text", "last_text"},
	{"terminal_event", "start", "terminal"},
	{"stream_duration", "first_event", "finish"},
	{"total", "start", "finish"},
}

func (t *timings) report(w io.Writer, secret string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	// Build once so each provider-controlled value is quoted and secret-redacted.
	var b strings.Builder
	b.WriteString("\nInference timings (ms; client-observed unless labeled provider):\n")
	for _, metric := range metricDefinitions {
		start := t.points[metric.from]
		if metric.from == "start" {
			start = t.start
		}
		fmt.Fprintf(&b, "  %-30s %s\n", metric.name, elapsed(start, t.points[metric.to]))
	}
	for _, kind := range []string{"dns", "tcp_connect", "tls"} {
		count := 0
		for _, p := range t.phases {
			if p.kind != kind {
				continue
			}
			count++
			fmt.Fprintf(&b, "  %s[%d]_ms = %s; at_ms = %s; failed = %t; address = %q\n", kind, count, elapsed(p.start, p.end), elapsed(t.start, p.start), p.failed, p.address)
		}
		if count == 0 {
			fmt.Fprintf(&b, "  %-30s unavailable\n", kind)
		}
	}
	fmt.Fprintf(&b, "  %-30s %s\n", "provider_processing_ms", providerMilliseconds(t.metadata["openai-processing-ms"]))
	for _, name := range []string{"requested_model", "response_model", "requested_service_tier", "actual_service_tier", "server-timing", "x-request-id", "http_protocol", "peer", "connection_reused", "connection_was_idle", "connection_idle_ms", "proxy_selected"} {
		value, ok := t.metadata[name]
		if !ok {
			value = "unavailable"
		}
		fmt.Fprintf(&b, "  %-30s %q\n", name, value)
	}
	byteCount := "unavailable"
	if !t.points["headers_received"].IsZero() {
		byteCount = strconv.FormatInt(t.bytes, 10)
	}
	fmt.Fprintf(&b, "  %-30s %s\n  %-30s %d\n", "response_body_bytes_read", byteCount, "text_delta_count", t.deltas)
	if len(t.gaps) == 0 {
		b.WriteString("  inter_delta_min/mean/max/p50/p95_ms unavailable\n")
	} else {
		gaps := append([]time.Duration(nil), t.gaps...)
		sort.Slice(gaps, func(i, j int) bool { return gaps[i] < gaps[j] })
		var sum float64
		for _, gap := range gaps {
			sum += float64(gap) / float64(time.Millisecond)
		}
		ms := func(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }
		percentile := func(p float64) float64 { return ms(gaps[int(math.Ceil(p*float64(len(gaps))))-1]) }
		fmt.Fprintf(&b, "  inter_delta_min/mean/max/p50/p95_ms %.3f / %.3f / %.3f / %.3f / %.3f (n=%d)\n", ms(gaps[0]), sum/float64(len(gaps)), ms(gaps[len(gaps)-1]), percentile(.5), percentile(.95), len(gaps))
	}
	if t.usage == nil {
		b.WriteString("  api_token_usage                unavailable\n")
	} else {
		count := func(value *int64) string {
			if value == nil {
				return "unavailable"
			}
			return strconv.FormatInt(*value, 10)
		}
		fmt.Fprintf(&b, "  api_token_usage                input=%d output=%d cached_input=%s cache_write_tokens=%s reasoning_output=%s\n", t.usage.input, t.usage.output, count(t.usage.cached), count(t.usage.cacheWrite), count(t.usage.reasoning))
		duration := t.points["finish"].Sub(t.start).Seconds()
		if duration > 0 {
			fmt.Fprintf(&b, "  output_tokens_per_total_second %.3f (aggregate, includes startup/wait)\n", float64(t.usage.output)/duration)
		}
	}
	text := b.String()
	if secret != "" {
		text = strings.ReplaceAll(text, secret, "[REDACTED]")
	}
	_, err := io.WriteString(w, text)
	return err
}
