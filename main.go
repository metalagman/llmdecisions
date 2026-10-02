package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.LookupEnv, os.ReadFile, nil, os.Stdout, os.Stderr))
}

func run(ctx context.Context, args []string, lookup func(string) (string, bool), readFile func(string) ([]byte, error), base http.RoundTripper, stdout, stderr io.Writer) int {
	cfg, err := loadConfig(args, lookup, readFile)
	if errors.Is(err, flag.ErrHelp) {
		_, err := fmt.Fprintln(stdout, "Usage: llmdecisions [-prompt prompt.txt] [-timeout 2m] [-inputs inputs.example.json] [-instructions file] [-cache-prefix] [-model gpt-5.6-luna] [-service-tier fast]\nOPENAI_API_KEY: environment takes precedence over optional .env.\nDefault model: gpt-5.6-luna; reasoning: none; Responses streaming; retries: 0.\nSingle mode: model text -> stdout; timings (ms) -> stderr.\nBatch mode: per-input JSONL -> stdout; full reports and comparison -> stderr.\n-cache-prefix: explicit cache breakpoint after the fixed developer instruction; requires -inputs and a prefix of at least 1024 tokens.")
		if err != nil {
			return 1
		}
		return 0
	}
	if err == nil {
		if len(cfg.inputs) > 0 {
			err = runBatch(ctx, cfg, base, stdout, stderr)
		} else {
			err = infer(ctx, cfg, base, stdout, stderr)
		}
	}
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)
		return 1
	}
	return 0
}
