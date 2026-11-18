# Troubleshooting the first run

[Русская версия](troubleshooting.ru.md)

Every message below was reproduced locally with a `vend` binary built from this
repository and, for the HTTP cases, the `tools/smokestub` rehearsal double on
`127.0.0.1`. No real Drex, Binance or Telegram call was made. The runs used a
dummy rehearsal token; no secret or token appears in the output. Paths, ports
and ids are placeholders (`<path>`, `<port>`, `<data-dir>`).

The exits codes are the ones printed by the binary in these runs: `0` success,
`1` general error, `2` usage/validation, `3` not found, `4` state conflict,
`5` forbidden, `6` provider error.

## Table: what you see → cause → what to do

| What you see | Cause | What to do |
|---|---|---|
| `starting on http://127.0.0.1:<port>` and then `vend: listen tcp 127.0.0.1:<port>: bind: Only one usage of each socket address (protocol/network address/port) is normally permitted.` / `hint: check the address is free and retry` (exit 1) | Another process already listens on the port. The startup line is printed before the bind; the server did not start. | Stop the process that owns the port or pass another `--addr 127.0.0.1:<free-port>`. Find the owner with `Get-NetTCPConnection -LocalPort <port>` on Windows or `ss -ltnp` on Linux. Do not kill processes by a wildcard name. |
| `vend: local API token is not set` / `hint: set VENDING_SECRET_CONN_LOCAL_APITOKEN and retry` (exit 5) | `vend serve` has no bearer token for the loopback API. | Set `VENDING_SECRET_CONN_LOCAL_APITOKEN` in the environment of the `vend serve` process and start it again. The token does not belong in the config. |
| `vend: cannot load config at <path>: file not found: <path>` / `hint: run vend init to create the config` (exit 3, `vend serve` and most commands) | The `--config` path does not exist. | Run `vend init`, or pass the path that `vend config path` prints. |
| `vend: config: read <path>: open <path>: The system cannot find the file specified.` / `hint: run vend init to create the config` (exit 2, `vend config validate`) | The same missing file, checked by `vend config validate`; this subcommand reports it as a validation error. | Run `vend init`, or pass an existing `--config`. |
| `vend: invalid_request: config: invalid JSON in <path>` / `hint: fix the JSON syntax or run vend init --force to recreate it` (exit 2) | The config file is not valid JSON (for example, it was cut off). | Fix the JSON, or recreate the file with `vend init --force`. Check it with `vend config validate`. |
| `vend: registry: unknown decision provider "decision-nope"` / `hint: align providers.decision.id with a registered provider` (exit 6) | `providers.decision.id` is not a provider registered in this build. | Use `decision-drex` in `providers.decision.id`; the core has no other decision provider in M1. Then run `vend config validate`. |
| `vend: invalid_request: config: live mode is not available in this release; use shadow, paper or sandbox` / `hint: fix the config value or run vend init --force to recreate it` (exit 2) | `mode` is `live`. | Set `mode` to `shadow` (the M1 mode) or another accepted value; there is no live order path. |
| HTTP 401, `{"code":"unauthorized","message":"missing or invalid bearer token","hint":"send Authorization: Bearer \u003ctoken\u003e","retryable":false}` | `POST /v1/gate` was sent without a token, with the wrong token, or with a non-bearer header. | Send `Authorization: Bearer <token>` with the value of `VENDING_SECRET_CONN_LOCAL_APITOKEN`. The token is compared in constant time; there is no other auth mode. |
| HTTP 400, `{"code":"bad_request","message":"invalid request body","hint":"send exactly one JSON gate request object","retryable":false}` | The request body is not one strict JSON object (broken JSON, trailing data, unknown fields). | Send exactly one JSON object; unknown fields are rejected. Use `contracts/examples/gate-request.json` as the shape. |
| HTTP 400, `{"code":"bad_request","message":"invalid gate request: state must be an object","hint":"fix the request fields and retry","retryable":false}` | A required field is missing or has the wrong type; this example omitted `state`. The message names the field that failed validation. | Add the named field. The required fields are `signalId`, `instrument{venue,symbol}`, `side`, `intentKind`, `timeframe`, `ts`, `price`, `source`, `state`. |
| HTTP 400, `{"code":"bad_request","message":"invalid gate request: unsupported timeframe \"4H\" (want a positive amount followed by a lowercase m, h, d or w, e.g. \"4h\")","hint":"fix the request fields and retry","retryable":false}` | The `timeframe` is not a positive amount followed by a lowercase `m`, `h`, `d` or `w` (for example `4H`, `1M`, `60`). Such a signal could never be labeled, so the gate rejects it and writes nothing to the journal. | Send a supported timeframe such as `5m`, `1h`, `4h`, `1d` or `1w`. A signal journaled by an older build with an unsupported timeframe is recorded once as `outcome.unlabelable` with reason `unsupported_timeframe`. |
| HTTP 400, `{"code":"bad_request","message":"invalid gate request: futures-style symbol \"BTC/USDT:USDT\" is not supported in M1 (spot only)","hint":"fix the request fields and retry","retryable":false}` | The symbol carries a futures suffix. M1 reads spot candles only, so such a signal could be evaluated but never labeled; the gate rejects it before any paid provider call. | Send the spot symbol (`BTC/USDT`, `BTC-USDT` or `BTCUSDT`). Futures support is not part of M1. |
| HTTP 200 with `"verdict":"ABSTAIN"`, `"code":"provider_auth"`, `"reasons":["provider auth error: provider rejected credentials or the quota is exhausted; check the provider secret (VENDING_SECRET_*) and quota"]` | The decision-provider secret is not set in the `vend serve` process, or the provider rejected it or the quota is exhausted. The gate still answers and journals the verdict. | Set `VENDING_SECRET_CONN_DREX_APIKEY` (the variable is derived from `providers.decision.secretRef`) in the `serve` environment; check the provider quota. `vend doctor --live` shows the same check without a signal. |
| `vend: database not found at <data-dir>/vending.db` / `hint: run vend init to create it` (exit 3, `vend gate report`, `vend status`, `vend journal ...`) | The journal does not exist yet, or `--data-dir`/`VENDING_DATA_DIR` points to another directory. | Run `vend init`, or pass the data directory that contains `vending.db`. |
| `output file already exists: <path> (pass --force to overwrite)` (exit 2) | `--out` or `--save-raw` would replace an existing file. | Pass `--force`, or choose a new path. A path inside the data directory is always refused: the journal and status file are protected. |
| `invalid --kind "<kind>"` (exit 2) | `journal tail --kind` got an event kind that does not exist. | Use one of the kinds listed in the hint (`details.allowed`), for example `gate.request` or `gate.verdict`. |
| `journal chain broken at seq N` (exit 1, `gate report` and `journal verify`) | The journal hash chain does not verify. | Restore the journal from backup. `gate report --allow-broken-chain` prints the report anyway, for recovery only. |
| `decision: unavailable (check the provider URL and network, then retry)` and `candles: unavailable (check the provider URL and network, then retry)` (exit 1, `vend doctor --live`) | The provider endpoint is unreachable: wrong `baseUrl`, the local stub is off, or there is no network. | Start the provider/stub, check `providers.decision.baseUrl` and `providers.marketdata.baseUrl`, then repeat. The full live-code table is in the [runbook](runbook-live-smoke.md#what-the-codes-mean). |
| `decision: auth (check the provider secret and quota)` (exit 1, `vend doctor --live`) | The decision-provider secret is missing or rejected. | Set `VENDING_SECRET_CONN_DREX_APIKEY` in the `doctor` environment and check the quota. |
| `vend: cannot use the journal database at <data-dir>/vending.db: unable to open database file (14)` / `hint: check the data directory is writable and retry` (exit 1) | The data directory exists but the process cannot write to it. | Choose a writable `--data-dir`/`VENDING_DATA_DIR`, or fix the directory permissions. On Windows 11 this was reproduced by denying write on a temporary directory with `icacls`; the deny entry was removed afterwards. |
| `FAIL trend: signals.trend.enabled is false; run vend init --quick or set it to true`, `FAIL symbols: ...`, `FAIL timeframe: ...`, `FAIL label_interval: ...`, `FAIL decision_secret: ...`, `FAIL secrets: ...` (exit 1, `vend doctor --strict`) | The installation is not configured for the G1 profile. | Fix the named field; see “Quick-start profile, G1 and demo” below. |
| `SYNTHETIC — not G1 evidence` and `g1: labels 0/100, active days 0/7` | The journal contains `vend demo` events. | Use a real data directory; see “Synthetic journal” below. |
| `last label age` above `expected label interval`, `labels due` above zero | The gate answers but the labeler is stuck. | See “Labels stopped” below. |

## Quick-start profile, G1 and demo

### `doctor --strict` fails

`--strict` requires the G1 profile; the first failing check is printed with
`FAIL <check>:` and the command exits 1. The field names and fixes:

| Message | Check | Fix |
|---|---|---|
| `FAIL trend: signals.trend.enabled is false; run vend init --quick or set it to true` | The config was written by the plain `init`, or trend was switched off. | Run `vend init --quick --config <path> --data-dir <dir>` for a new installation, or set `signals.trend.enabled` to `true` in the existing config. |
| `FAIL symbols: signals.trend.symbols has N symbols; the G1 profile needs at least 10` | Too few symbols, so the gate is silent or nearly silent. | Add symbols to `signals.trend.symbols`; the quick profile uses ten USDT pairs. |
| `FAIL timeframe: signals.trend.timeframe is "1h"; the G1 profile needs "15m"` | The profile check requires exactly `15m`; an unparseable value appends the parse error. | Set `signals.trend.timeframe` to `15m` (or use `init --quick`). |
| `FAIL label_interval: outcome.labelIntervalSec 600s must be at most half of signals.trend.timeframe 15m` | The label window is longer than half a bar. | Lower `outcome.labelIntervalSec` (300 in the quick profile) or use a longer timeframe. |
| `FAIL decision_secret: VENDING_SECRET_CONN_DREX_APIKEY is not set; the decision provider needs it` | The variable derived from `providers.decision.secretRef` is empty in this shell. | Set the variable; the name in the message is the exact one. |
| `FAIL decision_secret: providers.decision.secretRef is empty` | The config has no `secretRef`. | Set `providers.decision.secretRef` to `conn/drex/apiKey` (a reference, never the key). |
| `FAIL secrets: VENDING_SECRET_CONN_LOCAL_APITOKEN is not set; vend serve requires it` | The loopback API token is missing. | Set `VENDING_SECRET_CONN_LOCAL_APITOKEN` in the same shell that will run `serve`. |
| `FAIL config: ...`, `FAIL data_dir: ...`, `FAIL database: ...`, `FAIL journal: ...`, `FAIL providers: ...`, `FAIL gate_policy: ...` | A normal offline preflight failed. | Use the matching row in the main table above. |

Without `--strict`, a missing decision-provider secret is only a warning and
`doctor` exits 0; the local API token is always a hard failure.

A successful run ends with:

```text
profile ready; volume unverified
next: vend signals estimate --days 7
```

That is not a G1 verdict. The profile only proves the install is configured to
produce signals; signal volume needs the network estimate. With `--json` the
same fact is `"strict": true, "volumeVerified": false` plus
`"nextSteps": ["vend signals estimate --days 7"]`.

### Zero signals after a week

`vend init` without `--quick` writes `signals.trend.enabled: false` and an empty
`signals.trend.symbols`. In that state the gate answers only requests sent to
`POST /v1/gate`; if no bot sends them, the journal stays empty and G1 stays
`0/100, 0/7` forever.

Fix, without overwriting a tuned config:

```bash
vend init --quick --config <new-config> --data-dir <new-data-dir>
vend doctor --strict --config <new-config> --data-dir <new-data-dir>
vend signals estimate --days 7 --config <new-config> --data-dir <new-data-dir>
vend serve --config <new-config> --data-dir <new-data-dir>
```

`--force` on `init --quick` replaces the existing config and discards edits; use
a new `--config` path instead, or edit `signals.trend` by hand. Restart `serve`
after a config change: the config is read at startup.

`doctor --strict` passing does not mean the week will reach 100 labels. Check
the volume before starting:

```bash
vend signals estimate --days 7
```

`verdict: low` means roughly under 15 signals per day; add symbols or shorten
the timeframe (see [run-7-days.md](run-7-days.md), “Volume target”). A quiet
market can also produce no EMA crossing for a day: the estimate is history, not
a promise.

### Labels stopped

Symptom in `vend status`:

```text
g1: labels 41/100, active days 5/7, max gap 2h, eligible=no
last label age: 27h
expected label interval: 3.25h
labels due: 12
```

The gate keeps answering, but labels are older than the expected interval and
entry signals are past their labeling window. Check in this order:

1. `vend status --json` — `lastCycleError` must be empty and `updatedAt` must be
   fresh. A stale `updatedAt` means `serve` is not running.
2. `vend journal tail --kind outcome.unlabelable --limit 20` — the reason
   (`timeout`, `source_rejected`, `unsupported_timeframe`) names the failing
   step.
3. `vend doctor --live` — the `candles` probe checks the configured market-data
   endpoint. `unavailable` means a wrong `baseUrl`, no network or a dead stub;
   `rate_limit` means wait.
4. After fixing the cause, restart `serve`; pending outcomes are retried until
   their seven-day deadline, after which they become `outcome.unlabelable`.

`labels due: unknown` is not zero: it means the journal and the command cannot
derive the labeling window. Pass the same `--config` the runner uses (the
`horizonBars` and the signal timeframe are needed to compute the window).

### Synthetic journal

If `report`, `status` or `--share` says `SYNTHETIC — not G1 evidence` and
`g1: labels 0/100, active days 0/7`, the journal contains events written by
`vend demo` (`runId=demo`). The counters are zeroed on purpose, `eligible` is
false, and `gate report --share` exits 2 with
`synthetic journal is not G1 evidence`. `gate report --json` still prints the
numbers with `"synthetic": true`, but they are not evidence.

There is no flag that removes the mark and no supported way to edit it out: the
journal is hash-chained. Start the real run in a different data directory. Do
not point `serve` at a demo directory, and do not pass a real journal to
`vend demo --data-dir`.

## Collecting diagnostics

Run these from the repository root; they do not print secrets:

```bash
vend version
vend config validate
vend doctor
vend journal verify
vend status --json
```

For a failed HTTP call, keep the status code and the JSON body; the body has
`code`, `message`, `hint` and `retryable`. Do not send the config, environment
variables, `vending.db`, the `--save-raw` file, or screenshots with a token.
The `--share` report is sanitized and is the intended summary to publish.

## Freqtrade example

`examples/freqtrade/vend_shadow_gate.py` never blocks a trade: `confirm_trade_entry`
returns `True` even when the gate is off, busy, returns 401/500 or times out.
If nothing appears in `vend journal tail`, check `VEND_GATE_TOKEN` in the
Freqtrade process, `VEND_GATE_URL` (default `http://127.0.0.1:8420`) and that
`vend serve` is running. The example was checked against interface stubs, not
against a real Freqtrade installation; see the
[recipe](recipes/freqtrade.md).
