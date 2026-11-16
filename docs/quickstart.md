# Quickstart

Two paths to a working shadow gate:

- **Path A (15 minutes, with a decision key):** the loopback gate answers real
  requests and labels outcomes in the background.
- **Path B (offline, no key):** replay a recorded decision response through the
  same flow and verify the journal. No network, no credentials.

Everything below runs from the repository root and needs only Go (see `go.work`).
There are no paid services and no CI calls.

## Path A — 15 minutes with a decision key

### 1. Build and initialize

```bash
go build -o vend ./core/cmd/vend
./vend init
./vend version
```

`init` writes the JSON config, creates the SQLite journal and prints both paths.
On Windows the artifact is the literal file `vend` (no extension), so run
`.\vend`; build with `-o vend.exe` if you prefer.

### 2. Point the config at your provider

```bash
./vend config path      # absolute config path
./vend config validate  # validates without network or keys
```

Edit those values in the file (the rest stays as `init` wrote it); the relevant
fragment:

```json
{
  "providers": {
    "decision": {
      "id": "decision-drex",
      "model": "drex-max-v1.1",
      "profile": "drex",
      "secretRef": "conn/drex/apiKey"
    },
    "marketdata": {
      "id": "marketdata-binance",
      "baseUrl": "https://api.binance.com"
    }
  }
}
```

Rules enforced by validation:

- `model` must be a concrete id; aliases such as `*-latest` are rejected.
- `profile` is `drex` or `jev`.
- `secretRef` is a reference, never a key: `conn/<id>/<field>`.
- `marketdata.baseUrl` must be an http(s) URL (Binance spot by default).

The full key set — outcome labeling, Telegram, trend, arena — is described in
[spec/03-core-cli.md](spec/03-core-cli.md).

### 3. Set the secrets in the environment

The key is never written to the config, the journal or the logs. Each
`secretRef` maps to `VENDING_SECRET_<REF>`: uppercase, every character outside
`[A-Za-z0-9]` replaced by `_`. For `conn/drex/apiKey` that is
`VENDING_SECRET_CONN_DREX_APIKEY`.

Bash:

```bash
export VENDING_SECRET_CONN_DREX_APIKEY='<token>'
export VENDING_SECRET_CONN_LOCAL_APITOKEN='<token>'
```

PowerShell:

```powershell
$env:VENDING_SECRET_CONN_DREX_APIKEY = '<token>'
$env:VENDING_SECRET_CONN_LOCAL_APITOKEN = '<token>'
```

`VENDING_SECRET_CONN_LOCAL_APITOKEN` is the bearer token of the local API; the
server refuses to start without it. Use two different random values.

### 4. Start the shadow loop

```bash
./vend serve --addr 127.0.0.1:8420
```

The server binds loopback only. It also starts the background shadow runner:
every `outcome.labelIntervalSec` (default 300) it labels pending outcomes,
scans configured signals, and writes `shadow-status.json` into the data
directory. Keep the process running.

### 5. Send a gate request

```bash
curl -sS -X POST http://127.0.0.1:8420/v1/gate \
  -H "Authorization: Bearer <token>" \
  -H "Content-Type: application/json" \
  --data @contracts/examples/gate-request.json
```

On Windows PowerShell 5.1 `curl` is an alias for `Invoke-WebRequest`; call
`curl.exe` instead.

Response (HTTP 200):

```json
{
  "mode": "shadow",
  "signalId": "sig-001",
  "verdict": {
    "verdict": "ALLOW",
    "code": "allow",
    "reasons": ["all gate checks passed"],
    "checks": [],
    "decisionId": "<decision-id>",
    "model": "drex-max-v1.1",
    "policyHash": "<64 hex>",
    "decidedAt": "<RFC 3339>"
  }
}
```

`mode` is the configured mode (`shadow` by default) and is reported as-is.
The gate only evaluates: M1 places no orders, and `live` is rejected by config
validation.

The request body is a `GateRequest`:
`signalId`, `instrument{venue,symbol}`, `side` (`buy|sell`),
`intentKind` (`entry|exit`), `timeframe`, `ts`, `price > 0`, `source`, `state`
(map of finite numbers). Empty `signalId`, unknown fields and trailing JSON are
rejected.

### 6. Read the verdict

- `ALLOW` — every check passed within the policy thresholds.
- `REJECT` — the gate blocked the intent (CLI exits 7, HTTP stays 200).
- `ABSTAIN` — the provider failed or was uncertain; the verdict is still
  journaled and returned with HTTP 200.
- `SKIP` — evaluation was bypassed.

Every request appends `gate.request` → `decision.response` (when the provider
answered) → optional `decision.model_changed` → `gate.verdict`, all under one
`decisionId`.

### 7. Let outcomes label themselves

The runner labels every entry request (`intentKind: entry`) that has no outcome
yet. A label takes
the first bar opening at or after the signal time and closing after receipt,
exactly `outcome.horizonBars` consecutive bars, entry at the first open, exit at
the last close, net of `2 × outcome.feeBps` (default `feeBps: 10` per side, 20
round-trip). Raw provider candles come from `providers.marketdata`, so this
step needs network access to the exchange.

Signals that arrive later than one timeframe (`receivedAt − ts > timeframe`)
are marked `backfill`: they may enter a bar that had already closed when the
signal arrived, and they are excluded from report comparisons unless
`--include-backfill` is passed.

### 8. Report after enough labels

```bash
./vend gate report
```

The report is journal-only and needs no network. Below 100 labeled outcomes it
prints counters plus `insufficient data`; at 100 and above it adds group
returns, a bootstrap confidence interval (needs at least 5 distinct entry days),
Brier score, Brier skill score, AUC, ECE, the reliability curve and a per-model
breakdown (from 30 labels per model).

Useful variants:

```bash
./vend gate report --json                    # machine-readable report
./vend gate report --policy <64-hex-hash>    # restrict comparisons to one policy
./vend gate report --include-backfill        # include late samples in comparisons
./vend gate report --share                   # sanitized markdown summary
./vend gate report --share --out summary.md  # same, written with mode 0600
```

`--share` prints aggregate counts, model quality metrics and assumptions only:
no symbols, venues, prices, state, paths or identifiers. Publish it manually.
`--share` cannot be combined with `--json`; `--out` requires `--share`.

### 9. Status and integrity

```bash
./vend status          # runner snapshot + journal counters
./vend journal verify  # hash-chain check
```

`status` shows the version, event count, labeled count, last label time, the
policy hash prefix and the last cycle error. `journal verify` recomputes the
chain; a broken chain is an error with the sequence number.

## Path B — offline, no key

The same gate flow, driven by a recorded decision response:

```bash
go build -o vend ./core/cmd/vend
./vend init
./vend gate test \
  --request contracts/examples/gate-request.json \
  --fixture core/cmd/vend/testdata/decision-response.json
./vend journal verify
./vend gate report
```

The fixture is a synthetic cloud answer checked against the gate question set
exactly like a live response: option keys, probability ranges, unit sums,
argmax choice and recomputed confidence all have to match. `gate test` writes
the same journal events as a live evaluation, so the offline path is a safe way
to validate the wiring.

`gate report` after one request says:

```text
insufficient data: 0 labeled, need 100
```

That is expected: a fixture has no market data behind it. To label recorded
candles offline:

```bash
./vend outcomes label --candles candles.json
```

The file is a JSON array of candles (`symbol`, `interval`, `openTime`,
`closeTime`, `open`, `high`, `low`, `close`, `volume`; decimals as strings).
The labeler filters by symbol and interval and needs `horizonBars` consecutive
bars. Without `--candles` it uses the configured market data provider.

## `signalId` and idempotency

`signalId` is the idempotency key:

- The identical body sent again returns the recorded verdict with the same
  `decisionId`, without calling the provider.
- The same `signalId` with a **different** body is a conflict: HTTP 409
  `{"code":"conflict",...}`. Use a new `signalId` for a new signal.
- Concurrent duplicates of the same body share one provider call.
- `signalId` is required; an empty value is rejected.

The body fingerprint is the canonical JSON of the request, so field order and
whitespace do not matter, but any changed value does.

## Where state lives

Default paths (override with `--config`, `--data-dir`,
`VENDING_CONFIG_FILE`, `VENDING_DATA_DIR`; flag beats env):

| OS | Config | Data |
|---|---|---|
| Windows | `%APPDATA%\lv24-vending\config.json` | `%LOCALAPPDATA%\lv24-vending` |
| macOS | `~/Library/Application Support/lv24-vending/config.json` | `~/Library/Application Support/lv24-vending` |
| Linux | `$XDG_CONFIG_HOME/lv24-vending/config.json` | `$XDG_DATA_HOME/lv24-vending` |

The data directory holds `vending.db` (journal) and `shadow-status.json`.

## Troubleshooting

| Symptom | Cause and fix |
|---|---|
| `config already exists` (exit 4) | `vend init --force`, or pass another `--config` |
| `local API token is not set` (exit 5) | set `VENDING_SECRET_CONN_LOCAL_APITOKEN` |
| `secret "conn/..." is not set` (exit 5) | set the matching `VENDING_SECRET_*`, check with `vend doctor` |
| `invalid --addr` (exit 2) | host must be `127.0.0.1`, port 1–65535 |
| `signalId already used with a different request body` (409) | send a new `signalId` or the identical body |
| `insufficient data: N labeled, need 100` | not an error; keep the runner running |
| `--share cannot be combined with --json` / `--out requires --share` | fix the flags (exit 2) |
| `journal chain broken at seq N` | restore the journal from backup; it is append-only |

`vend doctor` checks the config, data directory, database, providers, secret
presence and the current gate policy hash without making network calls.
