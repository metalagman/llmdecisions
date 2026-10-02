package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptrace"
	"strings"
	"time"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/responses"
	"github.com/openai/openai-go/v3/shared"
)

func infer(ctx context.Context, cfg config, base http.RoundTripper, stdout, stderr io.Writer) (resultErr error) {
	return inferWithTimings(ctx, cfg, base, stdout, stderr, newTimings(time.Time{}))
}

func inferWithTimings(ctx context.Context, cfg config, base http.RoundTripper, stdout, stderr io.Writer, r *timings) (resultErr error) {
	if base == nil {
		transport := http.DefaultTransport.(*http.Transport).Clone()
		base = transport
		defer transport.CloseIdleConnections()
	}
	client := openai.NewClient(
		option.WithAPIKey(cfg.apiKey),
		option.WithBaseURL("https://api.openai.com/v1/"),
		option.WithMaxRetries(0),
		option.WithHTTPClient(&http.Client{
			Transport:     &timedTransport{base: base, timing: r},
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		}),
	)
	params := responses.ResponseNewParams{
		Model:     model,
		Input:     responses.ResponseNewParamsInputUnion{OfString: openai.String(cfg.prompt)},
		Reasoning: shared.ReasoningParam{Effort: shared.ReasoningEffortNone},
		Store:     openai.Bool(false),
	}
	if cfg.modelName != "" {
		params.Model = cfg.modelName
	}
	params.ServiceTier = responses.ResponseNewParamsServiceTier(cfg.serviceTier)
	r.mu.Lock()
	r.metadata["requested_model"] = string(params.Model)
	if cfg.serviceTier != "" {
		r.metadata["requested_service_tier"] = cfg.serviceTier
	}
	r.mu.Unlock()
	if cfg.cachePrefix {
		fixedContent := responses.ResponseInputMessageContentListParam{{
			OfInputText: &responses.ResponseInputTextParam{
				Text:                  cfg.instructions,
				PromptCacheBreakpoint: responses.NewResponseInputTextPromptCacheBreakpointParam(),
			},
		}}
		params.Input = responses.ResponseNewParamsInputUnion{OfInputItemList: responses.ResponseInputParam{
			responses.ResponseInputItemParamOfMessage(fixedContent, "developer"),
			responses.ResponseInputItemParamOfMessage(cfg.prompt, "user"),
		}}
		params.PromptCacheOptions = responses.ResponseNewParamsPromptCacheOptions{Mode: "explicit", Ttl: "30m"}
	} else if cfg.instructions != "" {
		params.Instructions = openai.String(cfg.instructions)
	}
	ctx, cancel := context.WithTimeout(ctx, cfg.timeout)
	defer cancel()
	ctx = httptrace.WithClientTrace(ctx, r.trace())
	r.start = time.Now()
	stream := client.Responses.NewStreaming(ctx, params)
	defer func() {
		if err := stream.Close(); err != nil && resultErr == nil {
			resultErr = errors.New("closing response stream failed")
		}
		r.mark("finish", time.Now())
		if err := r.report(stderr, cfg.apiKey); err != nil {
			resultErr = errors.New("writing timing report failed")
		}
	}()
	for stream.Next() {
		now := time.Now()
		r.mark("first_event", now)
		event := stream.Current()
		switch event.Type {
		case "response.output_text.delta":
			if event.Delta == "" {
				continue
			}
			r.text(now)
			if _, err := io.WriteString(stdout, event.Delta); err != nil {
				return errors.New("writing model output failed")
			}
		case "response.completed":
			r.mark("terminal", now)
			r.mu.Lock()
			if event.Response.JSON.ServiceTier.Valid() {
				r.metadata["actual_service_tier"] = string(event.Response.ServiceTier)
			}
			if event.Response.JSON.Model.Valid() {
				r.metadata["response_model"] = event.Response.Model
			}
			r.mu.Unlock()
			if event.Response.Status != "completed" {
				return errors.New("invalid status in completed response")
			}
			for _, item := range event.Response.Output {
				for _, content := range item.Content {
					if content.Type == "refusal" {
						return errors.New("model refused the request")
					}
				}
			}
			u := event.Response.Usage
			if event.Response.JSON.Usage.Valid() && u.JSON.InputTokens.Valid() && u.JSON.OutputTokens.Valid() {
				usage := &tokenUsage{input: u.InputTokens, output: u.OutputTokens}
				if u.InputTokensDetails.JSON.CachedTokens.Valid() {
					usage.cached = &u.InputTokensDetails.CachedTokens
				}
				if u.InputTokensDetails.JSON.CacheWriteTokens.Valid() {
					usage.cacheWrite = &u.InputTokensDetails.CacheWriteTokens
				}
				if u.OutputTokensDetails.JSON.ReasoningTokens.Valid() {
					usage.reasoning = &u.OutputTokensDetails.ReasoningTokens
				}
				r.mu.Lock()
				r.usage = usage
				r.mu.Unlock()
			}
			return nil
		case "response.failed":
			r.mark("terminal", now)
			return errors.New("model response failed")
		case "response.incomplete":
			r.mark("terminal", now)
			return errors.New("model response incomplete")
		case "response.refusal.delta", "response.refusal.done":
			return errors.New("model refused the request")
		case "error":
			return errors.New("API stream reported an error")
		}
	}
	if err := stream.Err(); err != nil {
		return inferenceError(ctx, err, cfg.apiKey)
	}
	return errors.New("response stream ended before completion")
}

func inferenceError(ctx context.Context, err error, secret string) error {
	if ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
			return errors.New("inference deadline exceeded")
		}
		return errors.New("inference canceled")
	}
	var apiErr *openai.Error
	if errors.As(err, &apiErr) {
		// Error.Error() includes the raw response body; only expose selected fields.
		message := fmt.Sprintf("OpenAI HTTP %d (type=%q code=%q)", apiErr.StatusCode, apiErr.Type, apiErr.Code)
		if secret != "" {
			message = strings.ReplaceAll(message, secret, "[REDACTED]")
		}
		return errors.New(message)
	}
	return errors.New("inference transport or stream decoding failed")
}
