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
| HTTP 200 with `"verdict":"ABSTAIN"`, `"code":"provider_auth"`, `"reasons":["provider auth error: provider rejected credentials or the quota is exhausted; check the provider secret (VENDING_SECRET_*) and quota"]` | The decision-provider secret is not set in the `vend serve` process, or the provider rejected it or the quota is exhausted. The gate still answers and journals the verdict. | Set `VENDING_SECRET_CONN_DREX_APIKEY` (the variable is derived from `providers.decision.secretRef`) in the `serve` environment; check the provider quota. `vend doctor --live` shows the same check without a signal. |
| `vend: database not found at <data-dir>/vending.db` / `hint: run vend init to create it` (exit 3, `vend gate report`, `vend status`, `vend journal ...`) | The journal does not exist yet, or `--data-dir`/`VENDING_DATA_DIR` points to another directory. | Run `vend init`, or pass the data directory that contains `vending.db`. |
| `decision: unavailable (check the provider URL and network, then retry)` and `candles: unavailable (check the provider URL and network, then retry)` (exit 1, `vend doctor --live`) | The provider endpoint is unreachable: wrong `baseUrl`, the local stub is off, or there is no network. | Start the provider/stub, check `providers.decision.baseUrl` and `providers.marketdata.baseUrl`, then repeat. The full live-code table is in the [runbook](runbook-live-smoke.md#what-the-codes-mean). |
| `decision: auth (check the provider secret and quota)` (exit 1, `vend doctor --live`) | The decision-provider secret is missing or rejected. | Set `VENDING_SECRET_CONN_DREX_APIKEY` in the `doctor` environment and check the quota. |
| `vend: cannot use the journal database at <data-dir>/vending.db: unable to open database file (14)` / `hint: check the data directory is writable and retry` (exit 1) | The data directory exists but the process cannot write to it. | Choose a writable `--data-dir`/`VENDING_DATA_DIR`, or fix the directory permissions. On Windows 11 this was reproduced by denying write on a temporary directory with `icacls`; the deny entry was removed afterwards. |

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
