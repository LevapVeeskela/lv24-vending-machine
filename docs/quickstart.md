# Quickstart

[Русская версия](quickstart.ru.md)

Three routes, shortest first:

| Route | Time | Needs | Counts toward G1 |
|---|---|---|---|
| `vend demo` | seconds | only the binary | no — synthetic journal |
| `vend init --quick` + `doctor --strict` | about 5 minutes | a decision key and internet for candles | yes, if it keeps running |
| Full walkthrough and offline replay | 15 minutes | a decision key, or a recorded fixture | only the live part |

G1 is the first gate: at least 100 labeled decisions on at least 7 UTC days with
no pause longer than 36 hours. `vend status` and `vend gate report` always print
the current progress as
`g1: labels N/100, active days D/7, max gap Hh, eligible=yes/no`; the details
are in [run-7-days.md](run-7-days.md).

Everything below runs from the repository root. There are no paid services and
no CI calls.

## Route 1 — see it in 60 seconds (`vend demo`)

`vend demo` writes a synthetic week into a temporary journal and prints the
full report over it. No key, no network, no provider call:

```bash
go build -o vend ./core/cmd/vend
./vend demo
```

On Windows build `vend.exe` and call `.\vend.exe demo`.

The journal has 7 UTC days, at least 120 decisions and at least 100 labeled
outcomes; probabilities and outcomes are deterministic (a fixed seed), and
Brier, ECE, AUC and the counterfactual are computed by the same report builder
as a real journal. Every major section carries the banner
`SYNTHETIC — not real market data`; the run finishes in seconds.

The demo journal is marked `runId=demo` and can never pass for real work:
`status` and `report` print `SYNTHETIC — not G1 evidence` with
`g1: 0/100, 0/7`, and `gate report --share` exits 2 with
`synthetic journal is not G1 evidence`.

Useful flags:

```bash
./vend demo --json                  # the same numbers with "synthetic": true
./vend demo --keep                  # keep the temporary data directory
./vend demo --data-dir ./demo-data  # explicit directory
```

Without `--keep` the temporary directory is deleted; with `--keep` the command
prints its path and the matching `vend status --data-dir <path>` command. The
`--data-dir` flag is only for a fresh directory: the synthetic marker is
permanent for any journal that contains the demo events, so never point it at a
real journal and never start a real G1 run in a demo directory.

## Route 2 — your own gate in 5 minutes (`init --quick`)

### 1. Get the binary

```bash
go build -o vend ./core/cmd/vend
./vend version
```

Windows PowerShell:

```powershell
go build -o vend.exe ./core/cmd/vend
.\vend.exe version
```

If you installed a prebuilt binary, skip the `go build` line and call `vend`.
The release is **not published** in this repository state, so the source build
is the working path; see [install](install.md).

### 2. Write the quick-start profile

```bash
./vend init --quick
```

Windows: `.\vend.exe init --quick`.

`init --quick` writes the config, creates the SQLite journal and prints both
paths, the profile name `g1-10x15m` and the next commands. The profile is the
default config plus one change — a signal source that actually produces
signals:

- `signals.trend.enabled: true` (the plain `init` leaves it false with no
  symbols, and a seven-day run then produces zero signals);
- ten USDT symbols: BTCUSDT, ETHUSDT, SOLUSDT, BNBUSDT, XRPUSDT, DOGEUSDT,
  ADAUSDT, LINKUSDT, AVAXUSDT, LTCUSDT;
- `timeframe: 15m`, `sides: ["buy", "sell"]`, `outcome.labelIntervalSec: 300`;
- gate, providers, fast/slow and fees stay at their defaults.

An existing config is not overwritten: `init --quick` exits 4. Pass `--force`
to replace it (this discards edits) or `--config <path>` to write another file.
`--json` adds `profile` and `nextSteps` to the usual fields; it contains no
secret values.

### 3. Set the two secrets

`init --quick` prints copy-paste templates for bash and PowerShell; the values
are `<token>` placeholders and are never written to disk. Set both variables in
the same shell that runs `doctor` and `serve`:

Bash — as printed by `init`:

```bash
export VENDING_SECRET_CONN_LOCAL_APITOKEN='<token>'
export VENDING_SECRET_CONN_DREX_APIKEY='<token>'
```

PowerShell — as printed by `init`:

```powershell
$env:VENDING_SECRET_CONN_LOCAL_APITOKEN = "<token>"
$env:VENDING_SECRET_CONN_DREX_APIKEY = "<token>"
```

`VENDING_SECRET_CONN_LOCAL_APITOKEN` is the bearer token of the local API; the
server refuses to start without it. `VENDING_SECRET_CONN_DREX_APIKEY` is the
decision-provider key, derived from the default `secretRef`
`conn/drex/apiKey`. Use two different random values. Each `secretRef` maps to
`VENDING_SECRET_<REF>`: uppercase, every character outside `[A-Za-z0-9]`
replaced by `_`.

### 4. Run the offline preflight

```bash
./vend doctor --strict
```

`--strict` requires the G1 profile and fails (exit 1, naming the field) when:

- `signals.trend.enabled` is false;
- fewer than 10 symbols are configured;
- the trend timeframe is not `15m` (or does not parse);
- `outcome.labelIntervalSec` is not at most half of the timeframe;
- `providers.decision.secretRef` is empty or its `VENDING_SECRET_*` is not set;
- `VENDING_SECRET_CONN_LOCAL_APITOKEN` is not set.

It also runs the normal offline checks: config, data directory, database,
journal chain, provider registry and gate policy hash. On success it prints
`profile ready; volume unverified` and the next command
`vend signals estimate --days 7`. That wording is deliberate: doctor proves the
installation is configured to produce signals, not that it produces enough of
them — only the network estimate can measure volume. `doctor` without
`--strict` keeps the earlier behaviour.

`--json` returns `ok`, `checks`, `strict: true`, `profile`,
`volumeVerified: false` and `nextSteps`.

### 5. Start the server

```bash
./vend serve --addr 127.0.0.1:8420
```

The server binds loopback only and starts the background runner: every
`outcome.labelIntervalSec` (300 s in this profile) it labels pending outcomes,
sends notifications and writes `shadow-status.json`; with `signals.trend`
enabled, trend scans run two seconds after each 15m bar closes. Keep the process
running.

Before the week, check how many signals your config produces:

```bash
./vend signals estimate --days 7
```

Aim for `verdict: enough`. The volume section of
[run-7-days.md](run-7-days.md) explains the numbers and what to change when the
estimate is low. Then continue with
[Send a gate request](#2-send-a-gate-request) below.

## Route 3 — the full walkthrough

The rest of this page is the detailed path: provider settings, the HTTP request,
verdicts, labeling, reports, status and the offline replay. The quick profile
needs none of the configuration edits in the first subsection.

### 1. Point the config at your provider

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

If you started from the plain `init` (no `--quick`), `signals.trend` is
disabled and `symbols` is empty, so the runner will never create a signal on its
own. Either run `init --quick --config <another-path>` and copy the
`signals.trend` block, or enable the trend source by hand and check it with
`vend doctor --strict`.

### 2. Send a gate request

```bash
curl -sS -X POST http://127.0.0.1:8420/v1/gate \
  -H "Authorization: Bearer <token>" \
  -H "Content-Type: application/json" \
  --data @contracts/examples/gate-request.json
```

On Windows PowerShell 5.1 `curl` is an alias for `Invoke-WebRequest`; call
`curl.exe` instead.

`contracts/examples/gate-request.json` is a template. Replace `signalId` with a
unique value and `ts` with the current UTC time: an old `ts` journals the signal
as `backfill` (it is counted for labeling but excluded from report comparisons
by default), and the same `signalId` replays the recorded verdict. Instead of
`<token>`, substitute `VENDING_SECRET_CONN_LOCAL_APITOKEN` (PowerShell:
`$env:VENDING_SECRET_CONN_LOCAL_APITOKEN`).

Response (HTTP 200):

```json
{
  "mode": "shadow",
  "signalId": "sig-001",
  "verdict": {
    "verdict": "ALLOW",
    "code": "allow",
    "reasons": ["all gate checks passed"],
    "checks": [
      { "name": "intent", "passed": true, "detail": "intentKind=entry" },
      { "name": "provider", "passed": true, "detail": "provider response ok" }
    ],
    "decisionId": "<decision-id>",
    "model": "drex-max-v1.1",
    "policyHash": "<64 hex>",
    "decidedAt": "<RFC 3339>"
  }
}
```

A real response fills `checks` with every check (`intent`, `provider`,
`answers`, `data_quality`, `risk_check`, `directional`, `confidence`); two are
shown above.

`mode` is the configured mode (`shadow` by default) and is reported as-is.
The gate only evaluates: M1 places no orders, and `live` is rejected by config
validation.

The request body is a `GateRequest`:
`signalId`, `instrument{venue,symbol}`, `side` (`buy|sell`),
`intentKind` (`entry|exit`), `timeframe`, `ts`, `price > 0`, `source`, `state`
(map of finite numbers). Empty `signalId`, unknown fields and trailing JSON are
rejected.

### 3. Read the verdict

- `ALLOW` — every check passed within the policy thresholds.
- `REJECT` — the gate blocked the intent (CLI exits 7, HTTP stays 200).
- `ABSTAIN` — the provider failed or was uncertain; the verdict is still
  journaled and returned with HTTP 200.
- `SKIP` — evaluation was bypassed.

Every request appends `gate.request` → `decision.response` (when the provider
answered) → optional `decision.model_changed` → `gate.verdict`, all under one
`decisionId`.

### 4. Let outcomes label themselves

The runner labels every entry request (`intentKind: entry`) that has no outcome
yet. A label takes the first bar opening at or after the signal time and closing
after the gate verdict (the receipt time when the journal has no verdict), exactly
`outcome.horizonBars` consecutive bars, entry at the first open, exit at
the last close, net of `2 × outcome.feeBps` (default `feeBps: 10` per side, 20
round-trip). If that first bar opened more than `min(10% of one bar, 30
seconds)` before the signal became actionable, the entry moves to the next bar
and the outcome records `entryShifted`; without a spare bar the signal waits
for a later cycle. `entryLagMs` is the delay from the entry-bar open to that
moment. Raw provider candles come from `providers.marketdata`, so this
step needs network access to the exchange.

With `horizonBars: 12` and the quick profile's `15m` bars the first label
appears after `(12 + 1) × 15m ≈ 3.25` hours. With the default `1h` timeframe it
takes about 13 hours. For a faster check use the
[runbook](runbook-live-smoke.md): `horizonBars: 3` and a manual `timeframe: "1m"`
request.

Signals that arrive later than one timeframe (`receivedAt − ts > timeframe`)
are marked `backfill`: they may enter a bar that had already closed when the
signal arrived, and they are excluded from report comparisons unless
`--include-backfill` is passed; the G1 label count never includes them.

### 5. Report after enough labels

```bash
./vend gate report
```

The report is journal-only and needs no network. It first verifies the journal
hash chain: a broken chain exits non-zero with the sequence number.
`--allow-broken-chain` still prints the report (recovery only, not for normal
use). Below 100 labeled outcomes it prints counters plus `insufficient data`.
From 100 labels it adds group returns and the bootstrap confidence interval; the
interval also requires at least 30 outcomes in each group and at least 5 distinct
entry days. Model quality (Brier, Brier skill score, AUC, ECE, reliability curve)
is computed only with at least 30 calibration pairs: below that each metric is
`n/a` and the line is `calibration pairs: N (need 30)`. The per-model breakdown
starts at 30 labels per model.

Right after the `window:` line the report prints the G1 progress block:

```text
g1: labels 23/100, active days 4/7, max gap 9.5h, eligible=no
last label age: 0.6h
expected label interval: 13h
labels due: 1
```

`labels` are the labeled outcomes of the audited policy (backfill is excluded
at any flag); `active days` counts UTC dates with a
`gate.verdict`; `max gap` is the longest pause between consecutive verdicts;
`eligible=yes` needs 100 labels, 7 days and a gap of at most 36 hours. `labels
due` counts entry signals whose labeling window has expired without either a
label or an `outcome.unlabelable` record; `unknown` means the window cannot be
derived from the journal. [run-7-days.md](run-7-days.md) explains how to read the
block and what counts as a failure.

Useful variants:

```bash
./vend gate report --json                    # machine-readable report
./vend gate report --policy <64-hex-hash>    # restrict comparisons to one policy
./vend gate report --include-backfill        # include late samples in comparisons
./vend gate report --share                   # sanitized markdown summary
./vend gate report --share --out summary.md  # same, written with mode 0600
./vend gate report --allow-broken-chain      # recovery only: report despite a broken chain
```

`--out` and `--save-raw` refuse to overwrite an existing file without `--force`
and refuse a path inside the data directory; the error names the reason and the
flag. `vending.db` and the config are never overwritten.

`--share` prints aggregate counts, the G1 lines, model quality metrics when
available, assumptions and the entry-lag median/p95 (as a fraction of one bar)
with the shifted-entry count. It also includes the aggregate ALLOW-vs-REJECT
mean return difference and its 95% interval. It contains no symbols, venues,
prices, position sizes, state, paths, identifiers or individual returns. The
summary has `days` (the acceptance-time window) and `active days` (distinct UTC
dates with at least one `gate.request`). A synthetic journal is refused: exit 2
with `synthetic journal is not G1 evidence`. Publish a real summary manually.
`--share` cannot be combined with `--json`; `--out` requires `--share`.

### 6. Status and integrity

```bash
./vend status          # runner snapshot + journal counters + G1 block
./vend journal verify  # hash-chain check
```

`status` shows the version, event count, labeled count, the G1 progress block,
the last label time, the policy hash prefix and the last cycle error.
`journal verify` recomputes the chain; a broken chain is an error with the
sequence number. Before the first `serve`, `status --json` omits empty
`policyHash`, `lastLabelAt`, `lastCycleError`, `updatedAt` and `lastSeq` instead
of printing `"0001-01-01T00:00:00Z"` or an empty string; the `g1` object is
still derived from the journal.

`vend help`, `vend help <command>`, `--help` and `-h` print help and exit 0; an
unknown command or help topic exits 2.

## Offline replay — no key

The same gate flow, driven by a recorded decision response. If `vend` is
already installed, skip the build line:

```bash
go build -o vend ./core/cmd/vend
./vend init
./vend gate test \
  --request contracts/examples/gate-request.json \
  --fixture core/cmd/vend/testdata/decision-response.json
./vend journal verify
./vend gate report
```

On Windows build `vend.exe` and use `.\vend.exe` with the same arguments.

The fixture is a synthetic cloud answer checked against the gate question set
exactly like a live response: option keys, probability ranges, unit sums,
argmax choice and recomputed confidence all have to match. `gate test` writes
the same journal events as a live evaluation, so the offline path is a safe way
to validate the wiring. It is not G1 evidence: the fixture has no market data
behind it, and a replay is not a new decision.

Repeating the same request with the same `signalId` is a replay: the verdict is
printed with `replayed: true`, the provider is not called again and no second
`gate.request` is written. A different `--fixture` for an already journaled
`signalId` is an error, not a silent replay: the command exits non-zero with
`fixture differs from journaled decision; change signalId or use another --data-dir`.

`gate report` after one request prints, among other lines:

```text
window: days=1 active=1
g1: labels 0/100, active days 1/7, max gap 0h, eligible=no
last label age: never
expected label interval: 13h
labels due: 0
labeled: 0
awaiting labeling: 1
insufficient data: 0 labeled, need 100
```

That is expected: a fixture has no market data behind it, so nothing is labeled
and the one verdict is today's answer. To label recorded
candles offline:

```bash
./vend outcomes label --candles candles.json
```

The file is a JSON array of candles (`symbol`, `interval`, `openTime`,
`closeTime`, `open`, `high`, `low`, `close`, `volume`; decimals as strings).
The labeler filters by canonical symbol and interval and needs `horizonBars`
consecutive bars. Without `--candles` it uses the configured market data
provider. Signals whose forward window has a missing or shifted bar stay
unlabeled and are counted as `unlabelable`.

## `signalId` and idempotency

`signalId` is the idempotency key:

- The identical body sent again returns the recorded verdict with the same
  `decisionId`, without calling the provider.
- The same `signalId` with a **different** body is a conflict: HTTP 409
  `{"code":"conflict",...}`. Use a new `signalId` for a new signal.
- Concurrent duplicates of the same body share one provider call.
- `gate test` marks a repeat of a journaled `signalId` with `replayed: true`; a
  different `--fixture` for that id exits non-zero instead of replaying the old
  verdict.
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

The data directory holds `vending.db` (journal) and `shadow-status.json`. A
journal that contains `vend demo` events stays synthetic forever: do not copy a
demo journal over a real one.

## Troubleshooting

| Symptom | Cause and fix |
|---|---|
| `config already exists` (exit 4) | `vend init --force`, or pass another `--config` |
| `local API token is not set` (exit 5) | set `VENDING_SECRET_CONN_LOCAL_APITOKEN` |
| `secret "conn/..." is not set` (exit 5) | set the matching `VENDING_SECRET_*`, check with `vend doctor` |
| `synthetic journal is not G1 evidence` (exit 2) | `--share` was run on a demo or mixed journal; report a real data directory |
| `invalid --addr` (exit 2) | host must be `127.0.0.1`, port 1–65535 |
| `signalId already used with a different request body` (409) | send a new `signalId` or the identical body |
| `insufficient data: N labeled, need 100` | not an error; keep the runner running |
| `--share cannot be combined with --json` / `--out requires --share` | fix the flags (exit 2) |
| `invalid --kind "x"` (exit 2) | the hint lists the allowed event kinds; use one of them |
| `output file already exists` (exit ≠0) | pass `--force` to replace it; a path inside the data directory is always refused |
| `journal chain broken at seq N` | the journal is damaged; `gate report` and `journal verify` exit non-zero. Restore it from backup; `gate report --allow-broken-chain` is for recovery only |

`vend doctor` checks the config, data directory, database, providers, secret
presence and the current gate policy hash without making network calls;
`--strict` adds the G1 profile invariants and never claims G1 without the
network volume estimate.

The full first-run table, with the actual messages and exit codes, is in
[troubleshooting](troubleshooting.md).
