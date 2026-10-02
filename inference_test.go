package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptrace"
	"os"
	"strings"
	"testing"
	"time"
)

const completedEvent = `{"type":"response.completed","response":{"status":"completed","usage":{"input_tokens":1155,"output_tokens":40,"input_tokens_details":{"cached_tokens":0},"output_tokens_details":{"reasoning_tokens":0}}}}`

func sse(events ...string) string {
	var b strings.Builder
	for _, event := range events {
		fmt.Fprintf(&b, "data: %s\n\n", event)
	}
	return b.String()
}

type testBody struct {
	io.Reader
	closed   int
	closeErr error
}

func (b *testBody) Close() error { b.closed++; return b.closeErr }

func TestInferencePayloadAndSuccess(t *testing.T) {
	prompt, err := os.ReadFile("prompt.txt")
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprintf("%x", sha256.Sum256(prompt)); got != "6138538d0baf361aedd33e7e5e259551f3dc4f7541932d0c0c2cb8a6e6a1b937" {
		t.Fatalf("pinned prompt changed: %s", got)
	}
	t.Setenv("OPENAI_BASE_URL", "https://unexpected.test/")
	body := &testBody{Reader: strings.NewReader(sse(
		`{"type":"response.created"}`,
		`{"type":"response.output_text.delta","delta":""}`,
		`{"type":"response.output_text.delta","delta":"{\"usage\":"}`,
		`{"type":"response.output_text.delta","delta":"0}"}`,
		completedEvent,
	))}
	calls := 0
	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		if req.Method != "POST" || req.URL.String() != "https://api.openai.com/v1/responses" {
			t.Fatalf("unexpected endpoint: %s %s", req.Method, req.URL)
		}
		var payload struct {
			Model, Input  string
			Stream, Store bool
			Reasoning     struct{ Effort string }
		}
		if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		if payload.Model != "gpt-5.6-luna" || payload.Input != string(prompt) || !payload.Stream || payload.Store || payload.Reasoning.Effort != "none" {
			t.Fatal("request differs from approved model/prompt/reasoning/stream/store")
		}
		if req.Header.Get("Authorization") != "Bearer sentinel-secret" {
			t.Fatal("incorrect credential")
		}
		trace := httptrace.ContextClientTrace(req.Context())
		trace.GetConn("api.openai.com:443")
		trace.GotConn(httptrace.GotConnInfo{Reused: true})
		trace.WroteHeaders()
		trace.WroteRequest(httptrace.WroteRequestInfo{})
		trace.GotFirstResponseByte()
		return &http.Response{StatusCode: 200, Proto: "HTTP/2.0", Header: http.Header{"Content-Type": {"text/event-stream"}, "Openai-Processing-Ms": {"750"}, "X-Request-Id": {"test-request"}}, Body: body}, nil
	})
	var out, diagnostics bytes.Buffer
	err = infer(context.Background(), config{apiKey: "sentinel-secret", prompt: string(prompt), timeout: time.Second}, transport, &out, &diagnostics)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || body.closed != 1 || out.String() != `{"usage":0}` {
		t.Fatalf("calls=%d close=%d output=%q", calls, body.closed, out.String())
	}
	flat := strings.Join(strings.Fields(diagnostics.String()), " ")
	for _, want := range []string{"api_token_usage input=1155 output=40 cached_input=0 cache_write_tokens=unavailable reasoning_output=0", "text_delta_count 2", "provider_processing_ms 750.000", "connection_reused \"true\"", "dns unavailable", "tls unavailable"} {
		if !strings.Contains(flat, want) {
			t.Errorf("missing %q:\n%s", want, diagnostics.String())
		}
	}
	if strings.Contains(diagnostics.String(), "sentinel-secret") {
		t.Fatal("credential leaked")
	}
	metrics := map[string]float64{}
	for _, line := range strings.Split(diagnostics.String(), "\n") {
		var name string
		var value float64
		if _, err := fmt.Sscan(line, &name, &value); err == nil {
			metrics[name] = value
		}
	}
	for _, pair := range [][2]string{{"ttfb", "first_stream_event"}, {"first_stream_event", "ttft_first_text"}, {"ttft_first_text", "terminal_event"}, {"terminal_event", "total"}} {
		a, okA := metrics[pair[0]]
		b, okB := metrics[pair[1]]
		if !okA || !okB || a > b {
			t.Errorf("invalid timing order %v", pair)
		}
	}
}

func TestInferenceFailureAndEmptyText(t *testing.T) {
	for _, tc := range []struct {
		name, event, wantErr string
		status               int
	}{
		{name: "no text", event: completedEvent, status: 200},
		{name: "failed", event: `{"type":"response.failed","response":{"status":"failed"}}`, status: 200, wantErr: "response failed"},
		{name: "incomplete", event: `{"type":"response.incomplete"}`, status: 200, wantErr: "response incomplete"},
		{name: "refusal", event: `{"type":"response.refusal.delta","delta":"sentinel-secret"}`, status: 200, wantErr: "refused"},
		{name: "final refusal", event: `{"type":"response.completed","response":{"status":"completed","output":[{"type":"message","content":[{"type":"refusal","refusal":"sentinel-secret"}]}]}}`, status: 200, wantErr: "refused"},
		{name: "invalid completed", event: `{"type":"response.completed","response":{"status":"in_progress"}}`, status: 200, wantErr: "invalid status"},
		{name: "stream error", event: `{"type":"error","message":"sentinel-secret"}`, status: 200, wantErr: "API stream"},
		{name: "EOF", event: `{"type":"response.created"}`, status: 200, wantErr: "ended before completion"},
		{name: "malformed", event: `{"type":`, status: 200, wantErr: "decoding failed"},
		{name: "bad request", status: 400, wantErr: "HTTP 400"},
		{name: "unauthorized", status: 401, wantErr: "HTTP 401"},
		{name: "rate limit no retry", status: 429, wantErr: "HTTP 429"},
		{name: "server error no retry", status: 500, wantErr: "HTTP 500"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			body := &testBody{Reader: strings.NewReader(sse(tc.event))}
			if tc.status != 200 {
				body.Reader = strings.NewReader(`{"error":{"message":"sentinel-secret","type":"sentinel-secret","code":"sentinel-secret"}}`)
			}
			tr := roundTripFunc(func(*http.Request) (*http.Response, error) {
				calls++
				return &http.Response{StatusCode: tc.status, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: body}, nil
			})
			var out, diag bytes.Buffer
			err := infer(context.Background(), config{apiKey: "sentinel-secret", prompt: "prompt", timeout: time.Second}, tr, &out, &diag)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error=%v want=%q", err, tc.wantErr)
			}
			if calls != 1 || body.closed != 1 {
				t.Fatalf("calls=%d closed=%d", calls, body.closed)
			}
			if strings.Contains(out.String()+diag.String()+fmt.Sprint(err), "sentinel-secret") {
				t.Fatal("secret leaked")
			}
			if !strings.Contains(strings.Join(strings.Fields(diag.String()), " "), "ttft_first_text unavailable") {
				t.Fatal("missing text misreported")
			}
			if !strings.Contains(diag.String(), "total") {
				t.Fatal("partial diagnostics missing")
			}
		})
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("sentinel-secret") }

func TestCancellationAndWriters(t *testing.T) {
	for _, timeout := range []bool{false, true} {
		ctx, cancel := context.WithCancel(context.Background())
		if !timeout {
			cancel()
		}
		tr := roundTripFunc(func(req *http.Request) (*http.Response, error) {
			<-req.Context().Done()
			return nil, req.Context().Err()
		})
		var diag bytes.Buffer
		err := infer(ctx, config{apiKey: "sentinel-secret", prompt: "prompt", timeout: time.Millisecond}, tr, io.Discard, &diag)
		cancel()
		want := "canceled"
		if timeout {
			want = "deadline exceeded"
		}
		if err == nil || !strings.Contains(err.Error(), want) || !strings.Contains(diag.String(), "total") {
			t.Fatalf("cancellation: %v", err)
		}
	}
	tr := roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(sse(`{"type":"response.output_text.delta","delta":"text"}`, completedEvent)))}, nil
	})
	for _, writers := range [][2]io.Writer{{failingWriter{}, io.Discard}, {io.Discard, failingWriter{}}} {
		if err := infer(context.Background(), config{apiKey: "key", prompt: "prompt", timeout: time.Second}, tr, writers[0], writers[1]); err == nil || strings.Contains(err.Error(), "sentinel-secret") {
			t.Fatalf("writer failure=%v", err)
		}
	}
}

func TestRunExitCodes(t *testing.T) {
	lookup := func(string) (string, bool) { return "sentinel-secret", true }
	read := func(path string) ([]byte, error) {
		if path == ".env" {
			return nil, os.ErrNotExist
		}
		return []byte("prompt"), nil
	}
	for _, tc := range []struct {
		args         []string
		status, want int
	}{{args: []string{"-help"}, want: 0}, {args: []string{"-timeout=0"}, want: 1}, {status: 200, want: 0}, {status: 500, want: 1}} {
		var out, diag bytes.Buffer
		tr := roundTripFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: tc.status, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(sse(completedEvent)))}, nil
		})
		if got := run(context.Background(), tc.args, lookup, read, tr, &out, &diag); got != tc.want {
			t.Fatalf("exit=%d want=%d", got, tc.want)
		}
		if strings.Contains(out.String()+diag.String(), "sentinel-secret") {
			t.Fatal("secret leaked")
		}
	}
}

func TestTransportFailureAndCloseFailure(t *testing.T) {
	for _, closeFailure := range []bool{false, true} {
		body := &testBody{Reader: strings.NewReader(sse(completedEvent)), closeErr: errors.New("sentinel-secret")}
		tr := roundTripFunc(func(*http.Request) (*http.Response, error) {
			if !closeFailure {
				return nil, errors.New("network failure containing sentinel-secret")
			}
			return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: body}, nil
		})
		var diagnostics bytes.Buffer
		err := infer(context.Background(), config{apiKey: "sentinel-secret", prompt: "prompt", timeout: time.Second}, tr, io.Discard, &diagnostics)
		if err == nil || strings.Contains(err.Error()+diagnostics.String(), "sentinel-secret") || !strings.Contains(diagnostics.String(), "total") {
			t.Fatalf("failure=%v: error or partial diagnostics missing/unsafe", closeFailure)
		}
		if closeFailure && body.closed != 1 {
			t.Fatal("stream body must close exactly once")
		}
	}
}
