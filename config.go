package main

import (
	"errors"
	"flag"
	"io"
	"os"
	"strings"
	"time"

	"github.com/joho/godotenv"
)

const model = "gpt-5.6-luna"

type config struct {
	apiKey       string
	prompt       string
	timeout      time.Duration
	instructions string
	inputs       []inputCase
	cachePrefix  bool
	modelName    string
	serviceTier  string
}

func loadConfig(args []string, lookup func(string) (string, bool), readFile func(string) ([]byte, error)) (config, error) {
	var cfg config
	flags := flag.NewFlagSet("llmdecisions", flag.ContinueOnError)
	// Flag errors can include supplied values; only return our safe error below.
	flags.SetOutput(io.Discard)
	promptPath := flags.String("prompt", "prompt.txt", "path to the full inference prompt")
	inputsPath := flags.String("inputs", "", "JSON array of named inputs for fixed-instruction batch")
	instructionsPath := flags.String("instructions", "", "fixed instruction file (batch only)")
	flags.BoolVar(&cfg.cachePrefix, "cache-prefix", false, "explicit cache boundary after fixed instruction (batch only)")
	flags.StringVar(&cfg.modelName, "model", model, "OpenAI model ID")
	flags.StringVar(&cfg.serviceTier, "service-tier", "", "processing tier: auto, default, fast or priority")
	flags.DurationVar(&cfg.timeout, "timeout", 2*time.Minute, "deadline including stream consumption")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return cfg, flag.ErrHelp
		}
		return cfg, errors.New("invalid arguments; use -help for usage")
	}
	if flags.NArg() != 0 || cfg.timeout <= 0 {
		return cfg, errors.New("expected flags only and a positive -timeout")
	}
	if cfg.cachePrefix && *inputsPath == "" {
		return cfg, errors.New("-cache-prefix requires -inputs")
	}
	if strings.TrimSpace(cfg.modelName) == "" {
		return cfg, errors.New("-model must not be empty")
	}
	switch cfg.serviceTier {
	case "", "auto", "default", "fast", "priority":
	default:
		return cfg, errors.New("invalid -service-tier; use auto, default, fast or priority")
	}
	values := map[string]string{}
	data, err := readFile(".env")
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return cfg, errors.New("cannot read .env")
	}
	if err == nil {
		values, err = godotenv.Unmarshal(string(data))
		if err != nil {
			return cfg, errors.New("invalid .env syntax")
		}
	}
	var present bool
	cfg.apiKey, present = lookup("OPENAI_API_KEY")
	if !present {
		cfg.apiKey = values["OPENAI_API_KEY"]
	}
	if strings.TrimSpace(cfg.apiKey) == "" {
		return cfg, errors.New("OPENAI_API_KEY is required in the environment or .env")
	}
	if *inputsPath == "" || *instructionsPath == "" {
		data, err = readFile(*promptPath)
		if err != nil {
			return cfg, errors.New("cannot read prompt file")
		}
		if strings.TrimSpace(string(data)) == "" {
			return cfg, errors.New("prompt file is empty")
		}
		cfg.prompt = string(data)
	}
	if *inputsPath == "" {
		if *instructionsPath != "" {
			return cfg, errors.New("-instructions requires -inputs")
		}
		return cfg, nil
	}
	if *instructionsPath != "" {
		data, err = readFile(*instructionsPath)
		if err != nil {
			return cfg, errors.New("cannot read instructions file")
		}
		cfg.instructions = string(data)
	} else {
		prefix, _, found := strings.Cut(cfg.prompt, "\nINPUT\n\n")
		if !found {
			return cfg, errors.New("prompt has no INPUT delimiter; provide -instructions")
		}
		cfg.instructions = prefix
	}
	if strings.TrimSpace(cfg.instructions) == "" {
		return cfg, errors.New("instructions file is empty")
	}
	data, err = readFile(*inputsPath)
	if err != nil {
		return cfg, errors.New("cannot read inputs file")
	}
	cfg.inputs, err = parseInputs(data)
	if err != nil {
		return cfg, err
	}
	return cfg, nil
}
