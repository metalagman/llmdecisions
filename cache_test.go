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

func TestExplicitCachePrefixAndUsage(t *testing.T) {
	fixed, err := os.ReadFile("instructions.cache.txt")
	if err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile("prompt.txt")
	if err != nil {
		t.Fatal(err)
	}
	prefix, _, _ := strings.Cut(string(original), "\nINPUT\n\n")
	if !strings.HasPrefix(string(fixed), prefix) {
		t.Fatal("expanded instruction lost original protocol")
	}
	cases, _ := parseInputs([]byte(`[{"id":"a","input":{"x":1}},{"id":"b","input":{"x":2}}]`))
	cfg := config{apiKey: "sentinel-secret", instructions: string(fixed), cachePrefix: true, inputs: cases, timeout: time.Second, modelName: "gpt-6-luna", serviceTier: "fast"}
	calls := 0
	tr := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		var payload struct {
			Model        string
			ServiceTier  string `json:"service_tier"`
			Instructions json.RawMessage
			Input        []struct {
				Role    string
				Content json.RawMessage
			}
			PromptCacheOptions struct{ Mode, Ttl string } `json:"prompt_cache_options"`
		}
		if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		if payload.Model != "gpt-6-luna" || payload.ServiceTier != "fast" || len(payload.Instructions) != 0 || len(payload.Input) != 2 || payload.Input[0].Role != "developer" || payload.Input[1].Role != "user" || payload.PromptCacheOptions.Mode != "explicit" || payload.PromptCacheOptions.Ttl != "30m" {
			t.Fatal("invalid explicit cache payload")
		}
		var content []struct {
			Type, Text string
			Breakpoint struct{ Mode string } `json:"prompt_cache_breakpoint"`
		}
		if err := json.Unmarshal(payload.Input[0].Content, &content); err != nil {
			t.Fatal(err)
		}
		if len(content) != 1 || content[0].Type != "input_text" || content[0].Text != string(fixed) || content[0].Breakpoint.Mode != "explicit" {
			t.Fatal("fixed boundary not serialized")
		}
		var input string
		if err := json.Unmarshal(payload.Input[1].Content, &input); err != nil || input != string(cases[calls].Input) {
			t.Fatal("variable input not preserved")
		}
		read, write := 0, 1600
		if calls == 1 {
			read, write = 1600, 0
		}
		calls++
		event := map[string]any{"type": "response.completed", "response": map[string]any{"status": "completed", "model": "gpt-6-luna", "service_tier": "fast", "usage": map[string]any{"input_tokens": 1700, "output_tokens": 40, "input_tokens_details": map[string]any{"cached_tokens": read, "cache_write_tokens": write}, "output_tokens_details": map[string]any{"reasoning_tokens": 0}}}}
		data, _ := json.Marshal(event)
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(sse(string(data))))}, nil
	})
	var out, diag bytes.Buffer
	if err := runBatch(context.Background(), cfg, tr, &out, &diag); err != nil {
		t.Fatal(err)
	}
	decoder := json.NewDecoder(&out)
	for i := 0; i < 2; i++ {
		var result batchResult
		if err := decoder.Decode(&result); err != nil {
			t.Fatal(err)
		}
		if result.Timings.Metadata["actual_service_tier"] != "fast" || result.Timings.Metadata["response_model"] != "gpt-6-luna" {
			t.Fatal("actual model/tier missing")
		}
		read, write := result.Timings.Usage["cached_input"], result.Timings.Usage["cache_write_tokens"]
		if read == nil || write == nil {
			t.Fatal("read/write usage missing")
		}
		if i == 0 && (*read != 0 || *write != 1600) || i == 1 && (*read != 1600 || *write != 0) {
			t.Fatal("read/write usage incorrect")
		}
	}
	for _, want := range []string{"cache_write_tokens=1600", "cache_write_tokens=0", "cached_input=1600", "input/output/cache_read/cache_write/reasoning"} {
		if !strings.Contains(diag.String(), want) {
			t.Errorf("report missing %s", want)
		}
	}
}

func TestCachePrefixFlagRequiresBatch(t *testing.T) {
	lookup := func(string) (string, bool) { return "key", true }
	read := func(string) ([]byte, error) { t.Fatal("invalid flag should fail before file reads"); return nil, nil }
	if _, err := loadConfig([]string{"-cache-prefix"}, lookup, read); err == nil || !strings.Contains(err.Error(), "requires -inputs") {
		t.Fatalf("flag error=%v", err)
	}
}
