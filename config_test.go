package main

import (
	"errors"
	"flag"
	"os"
	"strings"
	"testing"
	"time"
)

func TestLoadConfig(t *testing.T) {
	for _, tc := range []struct {
		name, env, dotenv, prompt, wantKey, wantErr string
		present                                     bool
		args                                        []string
		fileErr                                     error
	}{
		{name: "env wins", env: "env-key", present: true, dotenv: "OPENAI_API_KEY=file-key", prompt: " full\nprompt ", wantKey: "env-key"},
		{name: "dotenv quoting", dotenv: "export OPENAI_API_KEY='file-key'", prompt: "prompt", wantKey: "file-key"},
		{name: "optional file", env: "env-key", present: true, fileErr: os.ErrNotExist, prompt: "prompt", wantKey: "env-key"},
		{name: "explicit empty env wins", present: true, dotenv: "OPENAI_API_KEY=file-key", wantErr: "OPENAI_API_KEY is required"},
		{name: "missing key", fileErr: os.ErrNotExist, wantErr: "OPENAI_API_KEY is required"},
		{name: "malformed file", env: "env-key", present: true, dotenv: "!secret=do-not-print", wantErr: "invalid .env syntax"},
		{name: "unreadable dotenv", fileErr: os.ErrPermission, wantErr: "cannot read .env"},
		{name: "empty prompt", env: "env-key", present: true, prompt: " \n", wantErr: "prompt file is empty"},
		{name: "zero timeout", args: []string{"-timeout=0"}, wantErr: "positive -timeout"},
		{name: "negative timeout", args: []string{"-timeout=-1s"}, wantErr: "positive -timeout"},
		{name: "bad timeout", args: []string{"-timeout=secret"}, wantErr: "invalid arguments"},
		{name: "positional argument", args: []string{"secret"}, wantErr: "expected flags only"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lookup := func(string) (string, bool) { return tc.env, tc.present }
			read := func(path string) ([]byte, error) {
				if path == ".env" {
					return []byte(tc.dotenv), tc.fileErr
				}
				return []byte(tc.prompt), nil
			}
			cfg, err := loadConfig(tc.args, lookup, read)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error = %v, want %q", err, tc.wantErr)
				}
				if strings.Contains(err.Error(), "secret") {
					t.Fatalf("secret leaked: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if cfg.apiKey != tc.wantKey || cfg.prompt != tc.prompt || cfg.timeout != 2*time.Minute {
				t.Fatalf("unexpected configuration")
			}
		})
	}
}

func TestPromptFlagsAndErrors(t *testing.T) {
	lookup := func(string) (string, bool) { return "key", true }
	read := func(path string) ([]byte, error) {
		if path == "custom.txt" {
			return []byte("unaltered\nprompt"), nil
		}
		return nil, os.ErrNotExist
	}
	cfg, err := loadConfig([]string{"-prompt=custom.txt", "-timeout=3s"}, lookup, read)
	if err != nil || cfg.prompt != "unaltered\nprompt" || cfg.timeout != 3*time.Second {
		t.Fatalf("custom flags: %v", err)
	}
	if _, err := loadConfig(nil, lookup, read); err == nil || err.Error() != "cannot read prompt file" {
		t.Fatalf("missing prompt: %v", err)
	}
	if _, err := loadConfig([]string{"-help"}, lookup, read); !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("help: %v", err)
	}
}
