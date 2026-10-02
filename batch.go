package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"
)

type inputCase struct {
	ID    string          `json:"id"`
	Input json.RawMessage `json:"input"`
}

func parseInputs(data []byte) ([]inputCase, error) {
	var cases []inputCase
	if err := json.Unmarshal(data, &cases); err != nil || len(cases) == 0 {
		return nil, errors.New("inputs must be a nonempty JSON array of {id,input} objects")
	}
	seen := make(map[string]bool)
	for _, item := range cases {
		if strings.TrimSpace(item.ID) == "" || len(item.ID) > 128 || seen[item.ID] {
			return nil, errors.New("input IDs must be unique, nonempty and at most 128 bytes")
		}
		seen[item.ID] = true
		var object map[string]json.RawMessage
		if err := json.Unmarshal(item.Input, &object); err != nil || len(object) == 0 {
			return nil, errors.New("each input must be a nonempty JSON object")
		}
	}
	return cases, nil
}

type phaseRecord struct {
	Kind     string   `json:"kind"`
	Address  string   `json:"address,omitempty"`
	Start    *float64 `json:"start_ms"`
	Duration *float64 `json:"duration_ms"`
	Failed   bool     `json:"failed"`
}

type timingRecord struct {
	Metrics    map[string]*float64 `json:"metrics_ms"`
	Phases     []phaseRecord       `json:"network_phases"`
	Metadata   map[string]string   `json:"metadata"`
	Bytes      *int64              `json:"response_body_bytes_read"`
	DeltaCount int                 `json:"text_delta_count"`
	Gaps       []float64           `json:"inter_delta_gaps_ms"`
	Usage      map[string]*int64   `json:"api_token_usage"`
}

func milliseconds(from, to time.Time) *float64 {
	if from.IsZero() || to.IsZero() || to.Before(from) {
		return nil
	}
	value := float64(to.Sub(from)) / float64(time.Millisecond)
	return &value
}

func (t *timings) snapshot(secret string) timingRecord {
	t.mu.Lock()
	defer t.mu.Unlock()
	r := timingRecord{Metrics: make(map[string]*float64), Phases: []phaseRecord{}, Metadata: make(map[string]string), Gaps: []float64{}}
	for _, metric := range metricDefinitions {
		from := t.points[metric.from]
		if metric.from == "start" {
			from = t.start
		}
		r.Metrics[metric.name] = milliseconds(from, t.points[metric.to])
	}
	if value := providerMilliseconds(t.metadata["openai-processing-ms"]); value != "unavailable" {
		n, _ := strconv.ParseFloat(strings.TrimSpace(t.metadata["openai-processing-ms"]), 64)
		r.Metrics["provider_processing_ms"] = &n
	} else {
		r.Metrics["provider_processing_ms"] = nil
	}
	redact := func(s string) string {
		if secret == "" {
			return s
		}
		return strings.ReplaceAll(s, secret, "[REDACTED]")
	}
	for _, p := range t.phases {
		r.Phases = append(r.Phases, phaseRecord{Kind: p.kind, Address: redact(p.address), Start: milliseconds(t.start, p.start), Duration: milliseconds(p.start, p.end), Failed: p.failed})
	}
	for name, value := range t.metadata {
		r.Metadata[name] = redact(value)
	}
	if !t.points["headers_received"].IsZero() {
		n := t.bytes
		r.Bytes = &n
	}
	r.DeltaCount = t.deltas
	for _, gap := range t.gaps {
		r.Gaps = append(r.Gaps, float64(gap)/float64(time.Millisecond))
	}
	if t.usage != nil {
		copyInt := func(value *int64) *int64 {
			if value == nil {
				return nil
			}
			n := *value
			return &n
		}
		r.Usage = map[string]*int64{"input": copyInt(&t.usage.input), "output": copyInt(&t.usage.output), "cached_input": copyInt(t.usage.cached), "cache_write_tokens": copyInt(t.usage.cacheWrite), "reasoning_output": copyInt(t.usage.reasoning)}
	}
	return r
}

type batchResult struct {
	ID      string       `json:"id"`
	Output  string       `json:"output"`
	Error   string       `json:"error,omitempty"`
	Timings timingRecord `json:"timings"`
}

func runBatch(ctx context.Context, cfg config, base http.RoundTripper, stdout, stderr io.Writer) error {
	if base == nil {
		transport := http.DefaultTransport.(*http.Transport).Clone()
		defer transport.CloseIdleConnections()
		base = transport
	}
	var results []batchResult
	var failed bool
	encoder := json.NewEncoder(stdout)
	for _, item := range cfg.inputs {
		if ctx.Err() != nil {
			failed = true
			break
		}
		requestCfg := cfg
		requestCfg.prompt = string(item.Input)
		var out bytes.Buffer
		r := newTimings(time.Time{})
		id := strings.ReplaceAll(item.ID, cfg.apiKey, "[REDACTED]")
		if _, err := fmt.Fprintf(stderr, "\nCase %q\n", id); err != nil {
			return errors.New("writing case report failed")
		}
		err := inferWithTimings(ctx, requestCfg, base, &out, stderr, r)
		result := batchResult{ID: id, Output: out.String(), Timings: r.snapshot(cfg.apiKey)}
		if err != nil {
			result.Error = err.Error()
			failed = true
		}
		if err := encoder.Encode(result); err != nil {
			return errors.New("writing batch JSONL failed")
		}
		results = append(results, result)
	}
	if err := writeBatchSummary(stderr, results); err != nil {
		return errors.New("writing batch summary failed")
	}
	if failed {
		return errors.New("batch had failed or canceled cases; see per-case JSONL")
	}
	return nil
}

func writeBatchSummary(w io.Writer, results []batchResult) error {
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "\nBatch comparison (ms; unavailable phases are not zero):")
	fmt.Fprintln(tw, "case\tstatus\treused\tDNS\tTCP\tTLS\tTTFB\tfirst_text\tprovider\ttotal\tinput/output/cache_read/cache_write/reasoning")
	value := func(v *float64) string {
		if v == nil {
			return "n/a"
		}
		return fmt.Sprintf("%.3f", *v)
	}
	for _, r := range results {
		phases := map[string][]string{}
		for _, p := range r.Timings.Phases {
			phases[p.Kind] = append(phases[p.Kind], value(p.Duration))
		}
		phaseValue := func(kind string) string {
			if len(phases[kind]) == 0 {
				return "n/a"
			}
			return strings.Join(phases[kind], ",")
		}
		status := "ok"
		if r.Error != "" {
			status = "failed"
		}
		reused := r.Timings.Metadata["connection_reused"]
		if reused == "" {
			reused = "n/a"
		}
		count := func(name string) string {
			n := r.Timings.Usage[name]
			if n == nil {
				return "n/a"
			}
			return strconv.FormatInt(*n, 10)
		}
		fmt.Fprintf(tw, "%q\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s/%s/%s/%s/%s\n", r.ID, status, reused, phaseValue("dns"), phaseValue("tcp_connect"), phaseValue("tls"), value(r.Timings.Metrics["ttfb"]), value(r.Timings.Metrics["ttft_first_text"]), value(r.Timings.Metrics["provider_processing_ms"]), value(r.Timings.Metrics["total"]), count("input"), count("output"), count("cached_input"), count("cache_write_tokens"), count("reasoning_output"))
	}
	return tw.Flush()
}
