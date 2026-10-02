# llmdecisions — proof of concept

Экспериментальный CLI для проверки задержек, prompt caching и Fast mode.
Сохранённые результаты — разовые прогоны, а не production benchmark или гарантия скорости.

Go CLI для измерения задержек простого инференса через OpenAI Responses API.
Модель по умолчанию: `gpt-5.6-luna`, `reasoning.effort=none`, streaming,
`store=false`, без повторных попыток SDK. Никаких tools или agent loop.

## Результаты бенчмарков

Сохранённые прогоны выполнены **30 сентября 2026 года**: по восемь
последовательных запросов на одних и тех же входах из
[inputs.example.json](inputs.example.json). Reasoning — `none`, streaming включён,
SDK retries — 0. Каждый прогон использовал новый transport: первое соединение
новое, остальные семь — reused HTTP/2. Все 24 запроса завершились успешно.

Все времена в таблицах — **миллисекунды**. TTFT означает время до первого
непустого текстового события, TTFB — до первого байта ответа.

| Прогон | Медиана TTFB | Медиана TTFT | Медиана total | Диапазон total | Cache hits | Reasoning tokens |
|---|---:|---:|---:|---:|---:|---:|
| 5.6 Luna, исходная инструкция | 860.0 | 1328.7 | 1661.0 | 1166.4–2738.3 | 0/8 | 0 |
| 5.6 Luna, расширенная инструкция + кэш | 876.0 | 1208.0 | 1676.9 | 1142.0–2416.5 | 7/8 | 0 |
| 6 Luna fast, расширенная инструкция + кэш | 913.7 | 1070.3 | 1319.7 | 912.4–4369.1 | 7/8 | 0 |

Исходный фиксированный текст содержал 963 токена по локальному `o200k_base`.
Расширенный текст с примерами — 1591; API записал префикс размером 1594 токена.
В каждом прогоне с кэшем первый запрос записал 1594 токена, следующие семь
прочитали по 1594 без новых записей: 11158 из 13723 input tokens (81.3%).
GPT-6 Luna подтвердил фактический `service_tier: fast` во всех восьми ответах.
В старых прогонах фактический service tier не записывался; fast явно не запрашивался.

### Сравнение каждого входа

| Вход | 5.6 исходный TTFT | 5.6 кэш TTFT | 6 fast TTFT | 5.6 исходный total | 5.6 кэш total | 6 fast total |
|---|---:|---:|---:|---:|---:|---:|
| feature_flags | 2308.2 | 1864.1 | 1883.6 | 2738.3 | 2416.5 | 2188.2 |
| http_status | 1459.8 | 1331.0 | 951.7 | 1952.1 | 1800.2 | 1216.7 |
| returned_error | 1371.0 | 837.6 | 4116.9 | 1694.1 | 1222.2 | 4369.1 |
| numbers | 836.3 | 1799.3 | 837.5 | 1309.5 | 2313.5 | 1124.2 |
| handled_error | 926.0 | 806.3 | 1188.9 | 1247.5 | 1194.4 | 1422.7 |
| inventory | 1388.1 | 1257.8 | 618.2 | 1882.9 | 1814.5 | 912.4 |
| ignored_error | 1286.3 | 766.2 | 1256.8 | 1628.0 | 1142.0 | 1473.3 |
| constant_value | 786.1 | 1158.2 | 660.9 | 1166.4 | 1553.6 | 926.7 |

### Сеть и обработка API в GPT-6 Luna fast

| Вход | TTFB | TTFT | Processing API | Total | Cache read | Cache write |
|---|---:|---:|---:|---:|---:|---:|
| feature_flags | 1750.5 | 1883.6 | 853 | 2188.2 | 0 | 1594 |
| http_status | 786.3 | 951.7 | 437 | 1216.7 | 1594 | 0 |
| returned_error | 3998.5 | 4116.9 | 3381 | 4369.1 | 1594 | 0 |
| numbers | 716.1 | 837.5 | 406 | 1124.2 | 1594 | 0 |
| handled_error | 1041.1 | 1188.9 | 716 | 1422.7 | 1594 | 0 |
| inventory | 486.0 | 618.2 | 205 | 912.4 | 1594 | 0 |
| ignored_error | 1137.3 | 1256.8 | 809 | 1473.3 | 1594 | 0 |
| constant_value | 534.0 | 660.9 | 242 | 926.7 | 1594 | 0 |

Первое соединение в GPT-6 fast: DNS **97.0 мс**, TCP **5.9 мс**, TLS **19.5 мс**.
Для повторно используемых соединений эти этапы не выполнялись. TCP измеряет
установку соединения с peer, а не задержку полного пути до сервера инференса.
Processing API — заголовок `openai-processing-ms`; он не заменяет клиентское
измерение total и не позволяет выделить чистую сеть вычитанием из TTFB.

Медианы GPT-6 fast лучше предыдущего прогона с кэшем, однако `returned_error`
имеет выброс: TTFT **4116.9 мс**, Processing API **3381 мс**.
Это один проход на каждую конфигурацию, без повторов и рандомизации порядка.
В первом сравнении изменились инструкция и кэширование, во втором — модель
и service tier. Поэтому результаты не изолируют эффект кэша или Fast mode
и не являются SLA либо оценкой хвостовых задержек. Повторный запуск делает
платные API-вызовы и может дать другие значения.

### Исходные данные

| Прогон | Ответы и точные метрики | Все тайминги | Сравнение CSV |
|---|---|---|---|
| 5.6 исходный | [JSONL](results/batch.jsonl) | [Отчёт](results/timings.txt) | — |
| 5.6 с кэшем | [JSONL](results/cache-batch.jsonl) | [Отчёт](results/cache-timings.txt) | [CSV](results/cache-comparison.csv) |
| 6 fast с кэшем | [JSONL](results/luna6-fast-batch.jsonl) | [Отчёт](results/luna6-fast-timings.txt) | [CSV](results/luna6-fast-comparison.csv) |

Ответы сохранены без исправлений; измерения скорости не подтверждают
качество классификации. Полные отчёты содержат сетевые интервалы, события SSE,
интервалы между текстовыми delta, usage и request ID.

## Запуск

Нужен Go 1.26+. Запускайте из каталога проекта:

```bash
cp .env.example .env
# Впишите OPENAI_API_KEY в .env.
go run .
```

Если `.env` уже существует, редактируйте его вместо копирования примера.
Можно задать `OPENAI_API_KEY` в окружении: это значение имеет приоритет над `.env`,
даже если оно пустое. Отсутствующий `.env` допустим; неверный синтаксис существующего
файла — ошибка. `.env` и `.env.*` исключены из Git, кроме `.env.example`.
Ключ и необработанные тела ошибок API не выводятся.

По умолчанию отправляется полный `prompt.txt`, включая пример `INPUT`.
Текст ответа идёт в stdout, тайминги и ошибки — в stderr.

```bash
go run . -prompt prompt.txt -timeout 60s
go build -o llmdecisions .
./llmdecisions -help
```

Таймаут по умолчанию — 2 минуты на запрос, включая чтение stream.
Ctrl+C отменяет выполнение. HTTP/stream errors, отказ модели, incomplete response,
обрыв до завершения или ошибка записи дают ненулевой код возврата и частичные тайминги.
`OPENAI_BASE_URL` не меняет endpoint: CLI обращается к `https://api.openai.com/v1/`.
Обычные переменные HTTP(S)-proxy учитываются transport; URL и credentials proxy не печатаются.

## Фиксированная инструкция, несколько входов

```bash
mkdir -p results
go run . -inputs inputs.example.json -timeout 60s \
  > results/batch.jsonl 2> results/timings.txt
```

Инструкция берётся из `prompt.txt`: весь префикс перед разделителем `INPUT`, без
демонстрационного входа. Она передаётся в поле Responses `instructions` неизменно
для каждого запроса. Каждый объект из файла становится отдельным `input`.
Свою фиксированную инструкцию можно задать через `-instructions fixed.txt`;
в этом случае `prompt.txt` не требуется.

Формат входного файла:

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

`model` внутри протокола — `local-model`, как требует OUTPUT_SCHEMA из gist.
Модель API по умолчанию — `gpt-5.6-luna`; изменить её можно через `-model`. Содержимое `input` должно соответствовать
INPUT_SCHEMA. CLI до запроса проверяет формат списка, уникальные непустые `id`
(до 128 байт) и непустой объект `input`; полную проверку схемы поручает модели
согласно инструкции. Пустой или неверный список отклоняется до API-вызовов.

Запросы последовательны и используют один transport. Первое соединение может быть
новым, следующие — повторно используемыми: это видно в каждом отчёте. После ошибки
отдельного запроса следующие входы продолжают выполняться; Ctrl+C останавливает batch.
Если хотя бы один вход завершился ошибкой, весь запуск возвращает ненулевой код.

Stdout содержит JSONL: одна строка на вход с `id`, `output` (строка с ответом модели),
необязательным `error` и объектом `timings`. В нём:

- `metrics_ms`: временные отметки и интервалы; отсутствующие значения — `null`;
- `network_phases`: отдельные DNS/TCP/TLS-попытки с началом, длительностью и результатом;
- `metadata`: protocol, peer, reuse, proxy и разрешённые серверные заголовки;
- `text_delta_count` и `inter_delta_gaps_ms`: число непустых текстовых событий и все промежутки;
- `api_token_usage`: реальные input/output/cached/reasoning tokens и `cache_write_tokens`; отсутствующие поля — `null`;
- `response_body_bytes_read`: объём прочитанных HTTP body bytes, включая SSE metadata.

Stderr содержит полный отчёт для каждого `id`, затем сравнительную таблицу.
Текст модели в batch буферизуется для формирования одной JSONL-строки; одиночный
режим выводит текст по мере получения.

## Эксперимент с кэшированием фиксированного префикса

```bash
go run . -inputs inputs.example.json \
  -instructions instructions.cache.txt -cache-prefix -timeout 60s \
  > results/cache-batch.jsonl 2> results/cache-timings.txt
```

`-cache-prefix` работает только в batch. В этом режиме фиксированная инструкция
передаётся как `developer` message с явным `prompt_cache_breakpoint` на конце
текстового блока, а меняющийся JSON input — следующим `user` message.
Настройки запроса: `prompt_cache_options.mode=explicit`, `ttl=30m`.
Так кэшируется выбранный фиксированный префикс, без записи изменяющегося суффикса.
Обычный batch без флага продолжает использовать поле Responses `instructions`.

Для GPT-5.6 минимальный кэшируемый общий префикс — **1024 видимых токена**.
Исходная фиксированная инструкция содержит 963 текстовых токена по `o200k_base`:
общий input больше 1024, но его переменная часть уже различается между запросами.
В `instructions.cache.txt` сохранён исходный протокол с тремя полезными примерами:
игнорирование ошибки, возврат ошибки и неизвестный факт. Новый текст содержит
1591 токен по тому же tokenizer. Этот подсчёт выполнен локально; служебные границы
сообщений и полный отрендеренный контекст API сюда не входят. Go CLI не считает
токены локально: для другой инструкции размер префикса следует проверить отдельно.

`cached_input` — токены, **прочитанные** из кэша; `cache_write_tokens` — токены,
**записанные** в него. API сам возвращает эти значения. Отсутствующее поле
отмечается `null`/`unavailable`, а не нулём. Запись кэша тарифицируется отдельно;
актуальные правила описаны в [документации OpenAI](https://developers.openai.com/api/docs/guides/prompt-caching).

Результат сохранённого cache-эксперимента: первый запрос записал 1594 токена и
прочитал 0; следующие семь прочитали по 1594 токена и записали 0. Всего прочитано
11158 из 13723 input tokens (81.3%); все восемь запросов успешны, reasoning tokens
нулевые. Полные метрики — `results/cache-batch.jsonl`, отчёт —
`results/cache-timings.txt`, сравнение с исходным проходом —
`results/cache-comparison.csv`. Исходные `results/batch.jsonl` и `results/timings.txt`
сохранены без изменений.

В одном проходе медиана времени до первого текста была 1328.7 мс для исходной
инструкции и 1208.0 мс для расширенной. Это **не изолированная оценка ускорения от
кэша**: изменились сама инструкция и время запуска, а задержки сети/сервера меняются.
В частности, `numbers` с cache hit оказался медленнее своего исходного запроса.
Добавленные примеры также меняют ответы: `handled_error` теперь получил `noul=0.01`
вместо 0.98. Ответы обоих проходов сохранены без исправлений.

## Что именно измеряется

Все длительности в миллисекундах. Начало отсчёта — непосредственно перед вызовом SDK,
после чтения конфигурации и prompt и создания клиента. Метрики с суффиксом `_at`,
а также TTFB, первый stream event, первый текст, terminal event и total — от начала запроса.

| Метрика | Значение |
|---|---|
| `sdk_to_transport` | От вызова SDK до входа в transport, включая подготовку запроса SDK |
| DNS/TCP/TLS | Наблюдаемые `httptrace` интервалы; несколько TCP-попыток показаны отдельно |
| `connection_acquisition` | GetConn → GotConn, включая ожидание, DNS и handshakes |
| `request_write` | GotConn → успешный WroteRequest, включая scheduling transport |
| `headers_write_at`, `request_write_finished_at` | Отметки отправки заголовков и завершения попытки записи |
| `ttfb` | До первого наблюдаемого байта HTTP-ответа |
| `post_write_first_byte_wait` | Успешный WroteRequest → первый байт; включает сеть и серверное ожидание |
| `response_headers_at` | До возврата HTTP response headers из RoundTrip |
| `first_body_read_at`, `last_body_read_at` | До первого/последнего непустого чтения body |
| `body_eof_at`, `body_closed_at` | До EOF и закрытия body, если наблюдались |
| `first_stream_event` | До первого события, разобранного SDK |
| `ttft_first_text` | До первого непустого `response.output_text.delta` |
| `last_text_at`, `text_span` | До последнего текста; интервал первого → последнего текста |
| `terminal_event` | До completed/failed/incomplete event |
| `stream_duration` | Первое разобранное событие → завершение обработки/закрытия stream |
| `total` | Весь запрос до завершения обработки и закрытия stream; печать отчёта исключена |
| `provider_processing_ms` | Значение `openai-processing-ms`, сообщённое провайдером |
| `server-timing` | Неинтерпретированный `Server-Timing`, если заголовок присутствует |
| `inter_delta_min/mean/max/p50/p95_ms` | Клиентские интервалы текстовых событий; p50/p95 по nearest rank |
| `output_tokens_per_total_second` | Output tokens / total seconds: средняя скорость за весь запрос |

`unavailable` / `null` означает отсутствие наблюдения, а не ноль. Например, DNS/TCP/TLS
не выполняются для reused connection. Stream закрывается после терминального события,
поэтому `body_eof_at` может быть недоступен даже при успешном ответе.

Фазы могут пересекаться или уже входить в другие интервалы: их нельзя суммировать как
«полную сетевую задержку». TTFB минус processing time не является чистой задержкой сети.
Задержки промежуточных роутеров, server queue, prefill, GPU и отдельных токенов здесь не
измеряются. Серверный processing header не обязательно описывает весь streaming response.

Текстовый delta может содержать несколько токенов. SDK/HTTP buffering и scheduling
влияют на интервалы между событиями; они не равны времени генерации отдельного токена.
В одиночном режиме медленный stdout также влияет на последующие наблюдения.
Token usage из API выводится отдельно от `usage=0`, которое запрашивает сам prompt.

## Prompt и сохранённый эксперимент

`prompt.txt` — точная копия [gist](https://gist.github.com/metalagman/228cea2aa86dd1118ce78723fdfc7513),
полученная 30 сентября 2026 года. [Закреплённый raw файл](https://gist.githubusercontent.com/metalagman/228cea2aa86dd1118ce78723fdfc7513/raw/137f8ef050f667b0a21e78b11d74be546cbfcb29/gistfile1.txt):
4564 байта, SHA256 `6138538d0baf361aedd33e7e5e259551f3dc4f7541932d0c0c2cb8a6e6a1b937`.
API-запуск не загружает gist заново.

`inputs.example.json` содержит восемь разных входов, сгенерированных один раз с seed 56:
четыре примера кода, feature flags, HTTP status, inventory и числа. Они сохранены
для повторения того же эксперимента.

В `results/batch.jsonl` и `results/timings.txt` сохранён реальный восьмивходовый запуск:
8 успешных запросов, один новый и семь reused HTTP/2 connections, first text
786.122–2308.181 мс, total 1166.427–2738.271 мс. Cached input и reasoning tokens — 0
во всех случаях. Это один проход, а не оценка распределения задержек по многим повторам.
Ответы модели сохранены без исправления: в частности, `handled_error` получил `noul=0.98`
несмотря на показанную проверку ошибки. Измерение задержек не подтверждает качество классификации.

## Проверка

```bash
go test ./...
go test -race ./...
go vet ./...
go build ./...
```

Тесты не обращаются к внешнему API: проверяют конфигурацию, payload, фиксированную
инструкцию, SSE success/failure/refusal/EOF, отмену, ошибки записи, отсутствие retries,
nullable timings, конкурентные callback-вызовы, batch continuation и отсутствие
ключа в диагностике. Любой обычный одиночный или batch-запуск CLI делает реальные API-запросы.
Дополнительно проверяется сериализация явной границы кэша и наличие/отсутствие/нулевые
значения cache read/write usage; синтетические значения в тестах не считаются результатами эксперимента.

Документация: [модель и reasoning effort](https://developers.openai.com/api/docs/models/gpt-5.6-luna),
[Responses streaming](https://developers.openai.com/api/docs/guides/streaming-responses),
[серверные заголовки](https://developers.openai.com/api/reference/overview),
[OpenAI Go SDK](https://github.com/openai/openai-go),
[Go HTTP tracing](https://go.dev/blog/http-tracing).

## GPT-6 Luna с Fast mode

```sh
go run . -model gpt-6-luna -service-tier fast \
  -inputs inputs.example.json -instructions instructions.cache.txt -cache-prefix -timeout 60s \
  > results/luna6-fast-batch.jsonl 2> results/luna6-fast-timings.txt
```

`-service-tier` принимает `auto`, `default`, `fast`, `priority`; без флага поле
не отправляется. Reasoning остаётся `none`. Отчёт сохраняет запрошенную модель/режим
и фактические `response_model`/`actual_service_tier` из завершающего ответа API.
Отсутствующее значение обозначается как unavailable, а не как подтверждение Fast.
Fast оплачивается дороже стандартного режима; подробности в
[официальной документации](https://developers.openai.com/api/docs/models/gpt-6-luna).
