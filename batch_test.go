package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

func TestParseInputs(t *testing.T) {
	for _, data := range []string{`null`, `[]`, `{}`, `[{"id":"a","input":null}]`, `[{"id":"a","input":{}}]`, `[{"id":"","input":{"x":1}}]`, `[{"id":"a","input":{"x":1}},{"id":"a","input":{"x":2}}]`, `[{"id":"a","input":[]}]`} {
		if _, err := parseInputs([]byte(data)); err == nil {
			t.Errorf("accepted invalid inputs %s", data)
		}
	}
	data, err := os.ReadFile("inputs.example.json")
	if err != nil {
		t.Fatal(err)
	}
	cases, err := parseInputs(data)
	if err != nil || len(cases) != 8 {
		t.Fatalf("sample cases=%d error=%v", len(cases), err)
	}
	for _, item := range cases {
		var input struct {
			Model     string
			State     json.RawMessage
			Questions map[string]struct{ Type, Instructions string }
		}
		if err := json.Unmarshal(item.Input, &input); err != nil {
			t.Fatal(err)
		}
		if input.Model != "local-model" || len(input.State) == 0 || len(input.Questions) == 0 {
			t.Fatalf("invalid sample protocol %s", item.ID)
		}
		for _, q := range input.Questions {
			if q.Type != "noul" || q.Instructions == "" {
				t.Fatal("invalid sample question")
			}
		}
	}
}

func TestBatchFixedInstructionAndFailureContinuation(t *testing.T) {
	cases, _ := parseInputs([]byte(`[{"id":"failed","input":{"value":1}},{"id":"good","input":{"value":2}}]`))
	cfg := config{apiKey: "sentinel-secret", instructions: "fixed instruction", inputs: cases, timeout: time.Second}
	calls := 0
	tr := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		var payload struct {
			Instructions, Input string
			Reasoning           struct{ Effort string }
		}
		if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		if payload.Instructions != cfg.instructions || payload.Input != string(cases[calls].Input) || payload.Reasoning.Effort != "none" {
			t.Fatal("instructions/input changed")
		}
		calls++
		status := 200
		data := sse(`{"type":"response.output_text.delta","delta":"ok"}`, completedEvent)
		if calls == 1 {
			status = 500
			data = `{"error":{"message":"sentinel-secret"}}`
		}
		return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(data))}, nil
	})
	var out, diag bytes.Buffer
	if err := runBatch(context.Background(), cfg, tr, &out, &diag); err == nil {
		t.Fatal("failed case did not fail batch")
	}
	if calls != 2 {
		t.Fatalf("calls=%d; failure did not continue", calls)
	}
	decoder := json.NewDecoder(&out)
	for i := range cases {
		var result batchResult
		if err := decoder.Decode(&result); err != nil {
			t.Fatal(err)
		}
		if result.ID != cases[i].ID || result.Timings.Metrics["total"] == nil {
			t.Fatal("case correlation/timing lost")
		}
		if i == 0 && result.Error == "" {
			t.Fatal("missing per-case error")
		}
		if i == 1 && (result.Error != "" || result.Output != "ok") {
			t.Fatal("successful later case missing")
		}
		if result.Timings.Metrics["ttfb"] != nil {
			t.Fatal("unobserved timing fabricated")
		}
	}
	if strings.Contains(diag.String(), cfg.apiKey) || !strings.Contains(diag.String(), "Batch comparison") {
		t.Fatal("secret leak/missing summary")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	calls = 0
	if err := runBatch(ctx, cfg, tr, io.Discard, io.Discard); err == nil || calls != 0 {
		t.Fatal("canceled batch made request")
	}
}

func TestBatchConfigAndSnapshot(t *testing.T) {
	lookup := func(string) (string, bool) { return "key", true }
	read := func(path string) ([]byte, error) {
		switch path {
		case ".env":
			return nil, os.ErrNotExist
		case "prompt.txt":
			return []byte("fixed\nINPUT\n\nexample"), nil
		case "custom.txt":
			return []byte("custom fixed"), nil
		case "inputs.json":
			return []byte(`[{"id":"a","input":{"x":1}}]`), nil
		default:
			return nil, os.ErrNotExist
		}
	}
	for _, tc := range []struct {
		args []string
		want string
	}{{[]string{"-inputs=inputs.json"}, "fixed"}, {[]string{"-inputs=inputs.json", "-instructions=custom.txt"}, "custom fixed"}} {
		cfg, err := loadConfig(tc.args, lookup, read)
		if err != nil || cfg.instructions != tc.want || len(cfg.inputs) != 1 {
			t.Fatalf("config %v %v", tc.args, err)
		}
	}
	if _, err := loadConfig([]string{"-instructions=custom.txt"}, lookup, read); err == nil {
		t.Fatal("instructions without batch accepted")
	}
	withoutPrompt := func(path string) ([]byte, error) {
		if path == "prompt.txt" {
			return nil, os.ErrNotExist
		}
		return read(path)
	}
	if _, err := loadConfig([]string{"-inputs=inputs.json", "-instructions=custom.txt"}, lookup, withoutPrompt); err != nil {
		t.Fatalf("explicit instructions should not require prompt.txt: %v", err)
	}
	r := newTimings(time.Now())
	r.metadata["openai-processing-ms"] = " 1.25 "
	r.metadata["x-request-id"] = "key"
	snap := r.snapshot("key")
	if snap.Metrics["provider_processing_ms"] == nil || *snap.Metrics["provider_processing_ms"] != 1.25 || snap.Metrics["total"] != nil || snap.Usage != nil || snap.Metadata["x-request-id"] != "[REDACTED]" {
		t.Fatal("snapshot unavailable/provider/redaction incorrect")
	}
	r.metadata["x-request-id"] = "changed"
	if snap.Metadata["x-request-id"] != "[REDACTED]" {
		t.Fatal("snapshot aliases live map")
	}
}
