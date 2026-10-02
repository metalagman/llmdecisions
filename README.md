# llmdecisions — proof of concept

An experimental Go CLI for measuring inference latency, prompt caching, and
Fast mode through the OpenAI Responses API. The saved results are individual
runs, not a production benchmark or a speed guarantee.

Defaults: `gpt-5.6-luna`, `reasoning.effort=none`, streaming, `store=false`,
and no SDK retries. No tools or agent loop.

## Benchmark results

The saved runs were collected on **September 30, 2026**. Each configuration
processed the same eight inputs from [inputs.example.json](inputs.example.json)
sequentially, with reasoning set to `none`, streaming enabled, and zero retries.
Each run used a fresh transport: one new connection followed by seven reused
HTTP/2 connections. All 24 requests completed successfully.

All times in the tables are **milliseconds**. TTFT is time to the first nonempty
text event; TTFB is time to the first response byte.

| Run | Median TTFB | Median TTFT | Median total | Range total | Cache hits | Reasoning tokens |
|---|---:|---:|---:|---:|---:|---:|
| 5.6 Luna, original instruction | 860.0 | 1328.7 | 1661.0 | 1166.4–2738.3 | 0/8 | 0 |
| 5.6 Luna, expanded instruction + cache | 876.0 | 1208.0 | 1676.9 | 1142.0–2416.5 | 7/8 | 0 |
| 6 Luna fast, expanded instruction + cache | 913.7 | 1070.3 | 1319.7 | 912.4–4369.1 | 7/8 | 0 |

The original fixed text contained 963 tokens according to local `o200k_base`
tokenization. The expanded text contained 1591; the API cached a 1594-token prefix.
In each cached run, the first request wrote 1594 tokens and the next seven read
1594 tokens each without additional writes: 11158 of 13723 input tokens (81.3%).
GPT-6 Luna confirmed the actual `service_tier: fast` in all eight responses.
Earlier runs did not record the actual service tier and did not explicitly
request Fast mode.

### Per-input comparison

| Input | 5.6 baseline TTFT | 5.6 cached TTFT | 6 fast TTFT | 5.6 baseline total | 5.6 cached total | 6 fast total |
|---|---:|---:|---:|---:|---:|---:|
| feature_flags | 2308.2 | 1864.1 | 1883.6 | 2738.3 | 2416.5 | 2188.2 |
| http_status | 1459.8 | 1331.0 | 951.7 | 1952.1 | 1800.2 | 1216.7 |
| returned_error | 1371.0 | 837.6 | 4116.9 | 1694.1 | 1222.2 | 4369.1 |
| numbers | 836.3 | 1799.3 | 837.5 | 1309.5 | 2313.5 | 1124.2 |
| handled_error | 926.0 | 806.3 | 1188.9 | 1247.5 | 1194.4 | 1422.7 |
| inventory | 1388.1 | 1257.8 | 618.2 | 1882.9 | 1814.5 | 912.4 |
| ignored_error | 1286.3 | 766.2 | 1256.8 | 1628.0 | 1142.0 | 1473.3 |
| constant_value | 786.1 | 1158.2 | 660.9 | 1166.4 | 1553.6 | 926.7 |

### Network and API processing: GPT-6 Luna fast

| Input | TTFB | TTFT | API processing | Total | Cache read | Cache write |
|---|---:|---:|---:|---:|---:|---:|
| feature_flags | 1750.5 | 1883.6 | 853 | 2188.2 | 0 | 1594 |
| http_status | 786.3 | 951.7 | 437 | 1216.7 | 1594 | 0 |
| returned_error | 3998.5 | 4116.9 | 3381 | 4369.1 | 1594 | 0 |
| numbers | 716.1 | 837.5 | 406 | 1124.2 | 1594 | 0 |
| handled_error | 1041.1 | 1188.9 | 716 | 1422.7 | 1594 | 0 |
| inventory | 486.0 | 618.2 | 205 | 912.4 | 1594 | 0 |
| ignored_error | 1137.3 | 1256.8 | 809 | 1473.3 | 1594 | 0 |
| constant_value | 534.0 | 660.9 | 242 | 926.7 | 1594 | 0 |

The first GPT-6 fast connection took **97.0 ms DNS**, **5.9 ms TCP**, and
**19.5 ms TLS**. These phases did not run for reused connections. TCP measures
connection establishment with the peer, not the entire path to the inference
server. API processing comes from `openai-processing-ms`; it does not replace
client total time. Subtracting it from TTFB does not isolate network latency.

GPT-6 fast had better medians than the previous cached run, but `returned_error`
was an outlier: **4116.9 ms TTFT**, with **3381 ms API processing**.
There was only one pass per configuration, without repeated trials or randomized
order. The first comparison changed the instruction and caching; the second
changed the model and service tier. These results therefore do not isolate the
effect of caching or Fast mode and do not establish an SLA or tail latency.
Rerunning the commands makes billable API calls and may produce different results.

### Raw data

| Run | Responses and exact metrics | Full timings | Comparison CSV |
|---|---|---|---|
| 5.6 baseline | [JSONL](results/batch.jsonl) | [Report](results/timings.txt) | — |
| 5.6 with cache | [JSONL](results/cache-batch.jsonl) | [Report](results/cache-timings.txt) | [CSV](results/cache-comparison.csv) |
| 6 fast with cache | [JSONL](results/luna6-fast-batch.jsonl) | [Report](results/luna6-fast-timings.txt) | [CSV](results/luna6-fast-comparison.csv) |

Responses are preserved without corrections. Latency measurements do not validate
classification quality. The full reports include network intervals, SSE events,
text delta gaps, token usage, and request IDs.

## Quick start

Requires Go 1.26+. Run from the project directory:

```bash
cp .env.example .env
# Set OPENAI_API_KEY in .env.
go run .
```

If `.env` already exists, edit it rather than overwriting it with the example.
You can also set `OPENAI_API_KEY` in the environment; it takes precedence over
`.env`, including when empty. A missing `.env` is allowed; malformed syntax in
an existing file is an error. Git ignores `.env` and `.env.*`, except
`.env.example`. The key and raw API error bodies are not printed.

By default, the CLI sends all of `prompt.txt`, including its example `INPUT`.
Response text goes to stdout; timings and errors go to stderr.

```bash
go run . -prompt prompt.txt -timeout 60s
go build -o llmdecisions .
./llmdecisions -help
```

The default timeout is two minutes per request, including stream consumption.
Ctrl+C cancels execution. HTTP/stream errors, refusals, incomplete responses,
premature termination, and write failures produce a nonzero exit code and partial
timings. `OPENAI_BASE_URL` does not override the endpoint:
`https://api.openai.com/v1/`. The transport honors standard HTTP(S) proxy
variables; proxy URLs and credentials are not printed.

## Fixed instruction, multiple inputs

```bash
mkdir -p results
go run . -inputs inputs.example.json -timeout 60s \
  > results/batch.jsonl 2> results/timings.txt
```

The instruction is the prefix of `prompt.txt` before the `INPUT` delimiter,
excluding the example input. It is sent unchanged in the Responses `instructions`
field for each request. Each input object becomes a separate API input.
Use `-instructions fixed.txt` to supply a custom instruction; in that case,
`prompt.txt` is not required.

Input file format:

```json
[
  {
    "id": "example",
    "input": {
      "model": "local-model",
      "state": {"stock": 10, "order_quantity": 3},
      "questions": {
        "q1": {
          "type": "noul",
          "instructions": "Is the available stock sufficient to fulfill the order?"
        }
      }
    }
  }
]
```

The protocol's `model` is `local-model`, as required by the gist's OUTPUT_SCHEMA.
The API model defaults to `gpt-5.6-luna`; override it with `-model`.
The input must follow INPUT_SCHEMA. Before calling the API, the CLI validates
list structure, unique nonempty IDs of up to 128 bytes, and nonempty input
objects. Full schema validation is delegated to the model by the instruction.
Empty or malformed lists are rejected before API calls.

Requests run sequentially through one shared transport. Reports show connection
reuse. A failed request does not prevent subsequent inputs from running;
Ctrl+C stops the batch. Any failed input makes the batch exit with a nonzero code.

Stdout is JSONL: one record per input with `id`, `output` (the model's response
as a string), optional `error`, and a `timings` object containing:

- `metrics_ms`: timestamps and durations; unavailable values are `null`.
- `network_phases`: individual DNS/TCP/TLS attempts with start, duration, and outcome.
- `metadata`: protocol, peer, reuse, proxy, selected server headers, and recorded model/tier.
- `text_delta_count` and `inter_delta_gaps_ms`: nonempty text event count and all gaps.
- `api_token_usage`: actual input/output/cached/reasoning tokens and `cache_write_tokens`; missing fields are `null`.
- `response_body_bytes_read`: HTTP body bytes read, including SSE metadata.

Stderr contains a full report for each ID, followed by a comparison table.
Batch mode buffers model text to form a JSONL record; single mode prints text
as it arrives.

## Fixed-prefix caching experiment

```bash
go run . -inputs inputs.example.json \
  -instructions instructions.cache.txt -cache-prefix -timeout 60s \
  > results/cache-batch.jsonl 2> results/cache-timings.txt
```

`-cache-prefix` requires batch mode. The fixed instruction becomes a `developer`
message with an explicit `prompt_cache_breakpoint` at the end of its text block.
The variable JSON input follows as a `user` message. Request settings are
`prompt_cache_options.mode=explicit` and `ttl=30m`. This caches the fixed prefix
without writing the variable suffix. Batch mode without this flag continues
to use the Responses `instructions` field.

For GPT-5.6, the minimum reusable common prefix is **1024 visible tokens**.
The original instruction contains 963 text tokens according to `o200k_base`:
total input exceeds 1024, but the variable part already differs between requests.
[instructions.cache.txt](instructions.cache.txt) preserves the original protocol
and adds three examples: ignoring an error, returning an error, and an unknown
fact. Its text contains 1591 tokens by the same tokenizer. These local counts
exclude message framing and the full rendered API context. The CLI does not
count tokens locally; check the prefix length separately for custom instructions.

`cached_input` counts tokens **read** from the cache; `cache_write_tokens` counts
tokens **written** to it. Both come from the API. Missing fields are
`null`/`unavailable`, rather than zero. Cache writes are billed separately;
see the [OpenAI documentation](https://developers.openai.com/api/docs/guides/prompt-caching).

The saved caching experiment wrote 1594 tokens on the first request, then read
1594 on each subsequent request without additional writes. All eight requests
succeeded, with zero reasoning tokens. See the benchmark tables and linked
artifacts above. The baseline artifacts are preserved.

Median TTFT was 1328.7 ms with the original instruction and 1208.0 ms with the
expanded instruction. This is **not an isolated measurement of cache speedup**:
the instruction and execution time changed, while network/server latency varies.
For example, `numbers` was slower despite a cache hit. Added examples also
changed responses: `handled_error` returned `noul=0.01` rather than 0.98.
Both runs preserve the original responses without corrections.

## Timing definitions

Durations are in milliseconds. Measurement starts immediately before the SDK call,
after configuration/prompt loading and client creation. Metrics ending in `_at`,
TTFB, first stream event, first text, terminal event, and total are measured
from request start.

| Metric | Meaning |
|---|---|
| `sdk_to_transport` | SDK call to transport entry, including SDK request preparation |
| DNS/TCP/TLS | Observed `httptrace` intervals; multiple TCP attempts are listed separately |
| `connection_acquisition` | GetConn → GotConn, including waiting, DNS, and handshakes |
| `request_write` | GotConn → successful WroteRequest, including transport scheduling |
| `headers_write_at`, `request_write_finished_at` | Header write and request write completion timestamps |
| `ttfb` | Time to the first observed HTTP response byte |
| `post_write_first_byte_wait` | Successful WroteRequest → first byte; includes network and server waiting |
| `response_headers_at` | Time until RoundTrip returns response headers |
| `first_body_read_at`, `last_body_read_at` | Time to the first/last nonempty body read |
| `body_eof_at`, `body_closed_at` | Time to body EOF and closure, when observed |
| `first_stream_event` | Time to the first event parsed by the SDK |
| `ttft_first_text` | Time to the first nonempty `response.output_text.delta` |
| `last_text_at`, `text_span` | Last text timestamp; interval from first to last text |
| `terminal_event` | Time to a completed/failed/incomplete event |
| `stream_duration` | First parsed event → end of stream handling/closure |
| `total` | Entire request through stream handling and closure; excludes report printing |
| `provider_processing_ms` | Provider-reported `openai-processing-ms` value |
| `server-timing` | Raw `Server-Timing` header, if supplied |
| `inter_delta_min/mean/max/p50/p95_ms` | Client text event gaps; p50/p95 use nearest rank |
| `output_tokens_per_total_second` | Output tokens / total seconds, including startup and waiting |

`unavailable`/`null` means no observation, not zero. DNS/TCP/TLS do not run for
reused connections. The stream closes after the terminal event, so `body_eof_at`
may be unavailable even for successful responses.

Phases can overlap or be included in other intervals; adding them does not give
complete network latency. TTFB minus processing time is not pure network latency.
Intermediate router delays, server queueing, prefill, GPU time, and individual
token generation are not measured here. The processing header does not
necessarily describe the entire streaming response.

A text delta can contain multiple tokens. SDK/HTTP buffering and scheduling affect
inter-event gaps, which are not per-token generation times. Slow stdout also
affects subsequent observations in single mode. API token usage is recorded
separately from the `usage=0` requested by the prompt's output protocol.

## Prompt and saved experiment

[prompt.txt](prompt.txt) is an exact copy of the
[gist](https://gist.github.com/metalagman/228cea2aa86dd1118ce78723fdfc7513),
retrieved on September 30, 2026.
[Pinned raw file](https://gist.githubusercontent.com/metalagman/228cea2aa86dd1118ce78723fdfc7513/raw/137f8ef050f667b0a21e78b11d74be546cbfcb29/gistfile1.txt):
4564 bytes, SHA256 `6138538d0baf361aedd33e7e5e259551f3dc4f7541932d0c0c2cb8a6e6a1b937`.
API runs do not download the gist again.

[inputs.example.json](inputs.example.json) contains eight inputs generated once
with seed 56: four code examples, feature flags, HTTP status, inventory, and
numbers. They are retained to reproduce the experiment.

The baseline artifacts contain eight successful requests: one new and seven
reused HTTP/2 connections, TTFT 786.122–2308.181 ms, and total time
1166.427–2738.271 ms. Cached input and reasoning tokens were zero throughout.
This is one pass, not a latency distribution across repeated trials.
Responses are preserved unchanged: `handled_error` returned `noul=0.98` despite
the error check in the input. Latency results do not validate classification quality.

## Verification

```bash
go test ./...
go test -race ./...
go vet ./...
go build ./...
```

Tests do not call the external API. They cover configuration, request payloads,
fixed instructions, SSE success/failure/refusal/EOF, cancellation, write failures,
no retries, nullable timings, concurrent trace callbacks, batch continuation,
and secret-safe diagnostics. Normal single or batch CLI runs make real API calls.
Tests also check explicit cache boundary serialization and present/missing/zero
cache read/write values. Synthetic test values are not benchmark results.

References: [model and reasoning effort](https://developers.openai.com/api/docs/models/gpt-5.6-luna),
[Responses streaming](https://developers.openai.com/api/docs/guides/streaming-responses),
[server headers](https://developers.openai.com/api/reference/overview),
[OpenAI Go SDK](https://github.com/openai/openai-go),
and [Go HTTP tracing](https://go.dev/blog/http-tracing).

## GPT-6 Luna with Fast mode

```sh
go run . -model gpt-6-luna -service-tier fast \
  -inputs inputs.example.json -instructions instructions.cache.txt -cache-prefix -timeout 60s \
  > results/luna6-fast-batch.jsonl 2> results/luna6-fast-timings.txt
```

`-service-tier` accepts `auto`, `default`, `fast`, and `priority`. Without the flag,
the field is omitted. Reasoning remains `none`. Reports record the requested
model/tier and the actual `response_model`/`actual_service_tier` from the terminal
API response. A missing value is marked unavailable, not treated as proof of Fast
mode. Fast costs more than Standard processing; see the
[official documentation](https://developers.openai.com/api/docs/models/gpt-6-luna).
