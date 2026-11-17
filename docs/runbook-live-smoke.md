# Runbook: live check of the shadow gate (15 minutes)

[Русская версия](runbook-live-smoke.ru.md)

Goal: the owner, on their own machine, checks that `vend` really talks to Drex
and Binance, labels outcomes and writes a report. No orders are sent, no money
moves: M1 runs only in `shadow`.

The check takes 15 minutes, of which ~6 minutes are waiting for one manual
request with the `1m` timeframe to be labeled. Everything that should be sent
for review is listed at the end; secrets, the config and the journal are not to
be sent.

## Prerequisites

- `vend` is installed and `vend version` prints a version
  ([install.md](install.md)).
- `vend init` has been run; the config and data paths are known
  (`vend config path`, the data directory is next to `vending.db`).
- The environment has a decision-provider key and a local API token:

  ```bash
  export VENDING_SECRET_CONN_DREX_APIKEY='<token>'
  export VENDING_SECRET_CONN_LOCAL_APITOKEN='<token>'
  ```

  PowerShell: `$env:VENDING_SECRET_CONN_DREX_APIKEY = '<token>'` and likewise
  for `VENDING_SECRET_CONN_LOCAL_APITOKEN`. The values are different random
  strings.
- Secrets never get into files, screenshots or messages.

## Step 0. Synchronize the system clock

The verdict time and the signal receipt time define the labeling window, while
the notification schedule and `entryLagMs` are computed in UTC. A wrong clock
shifts the label and the alerts, so synchronize the time first:

- Windows: `w32tm /resync` (or Settings → Time & language → Date & time →
  Sync now).
- Linux: `sudo timedatectl set-ntp true`, then check `timedatectl`.
- macOS: System Settings → General → Date & Time → Set date and time
  automatically.

## Step 1. Live doctor probes (5 minutes)

First the offline check, then the live calls. Save the output immediately: this
same file goes to review, and step 5 does not need a second paid call.

Linux / macOS:

```bash
vend doctor 2>&1 | tee doctor.txt
vend doctor --live --symbol BTCUSDT --save-raw <file> 2>&1 | tee -a doctor.txt
```

Windows PowerShell (no `tee`; `*>` overwrites, `*>>` appends):

```powershell
.\vend.exe doctor *> doctor.txt
.\vend.exe doctor --live --symbol BTCUSDT --save-raw <file> *>> doctor.txt
```

`--live` performs three optional network probes, each with a 20 second
deadline:

| Probe | What it does | Success |
|---|---|---|
| `decision` | one decision-provider call with a fixed synthetic state and the gate question set; the answer goes through `gate.Evaluate` | `decision: ok verdict=<ALLOW\|REJECT\|ABSTAIN> code=<code> cost=<decimal> USD` |
| `candles` | two closed `1h` candles for `--symbol` (default `BTCUSDT`) from the marketdata provider | `candles: ok` |
| `telegram` | one test message, only when `notify.telegram.enabled` | `telegram: ok` |

`ABSTAIN` with the code `invalid_output` is a **FAIL** of the `decision` probe:
the answer did not pass the gate question check. The other verdicts (`ALLOW`,
`REJECT`, `ABSTAIN` with `low_confidence`/`insufficient_data` and so on) mean
the live call and the policy worked, and the probe succeeded. Example of a
successful line:
`decision: ok verdict=ALLOW code=allow cost=0.0001 USD`.

`--save-raw <file>` writes the raw decision-provider answer atomically with
permissions 0600 and prints `save_raw: ok`. The file may contain vendor service
data: do not commit or forward it.

`--symbol` and `--save-raw` require `--live`; without it the command exits with
code 2.

### What the codes mean

The `decision` line prints `ok verdict=... code=... cost=... USD` or a short
error code; the other probes print `ok` or a short error code:

| Code | Meaning | What to do |
|---|---|---|
| `ok` | the probe passed | — |
| `auth` | missing or wrong key, or the quota is exhausted | check `secretRef` and `VENDING_SECRET_*`, the provider balance/quota |
| `payment_required` | the provider requires payment or quota | top up/enable the quota at the provider |
| `rate_limit` | throttling | wait and retry later |
| `invalid_request` | the provider rejected the request: model, profile, URL | compare `providers.decision.*` with `vend config validate` |
| `invalid_output` | the answer does not match the contract | retry; if it repeats, report with `--save-raw` |
| `unavailable` | temporary unavailability or network | retry in a few minutes |
| `timeout` | did not finish within 20 seconds | check the network/provider, retry |
| `ip_banned` | the IP is banned by the provider | do not retry; change the network/provider |
| `region_unavailable` | the region is unavailable | use an available region/provider |
| `unknown` | not classified, printed as `unavailable` | retry and report |

If `config` is invalid, the live probes return `invalid_request`; if the
provider registry did not assemble, they return `unavailable`. Any FAIL probe
gives exit code 1. `--json` prints
`{"ok":bool,"checks":[{name,ok,detail,live?}]}` — convenient to attach to the
report.

## Step 2. Config for the check (2 minutes)

In the config (`vend config path`) enable trend and a short horizon. The
fragment below lists only the keys to change; leave the rest as `init` created
it:

```json
{
  "mode": "shadow",
  "outcome": {
    "horizonBars": 3,
    "feeBps": 10,
    "labelIntervalSec": 30
  },
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
  },
  "signals": {
    "trend": {
      "enabled": true,
      "venue": "binance",
      "symbols": ["BTCUSDT"],
      "timeframe": "5m",
      "fast": 12,
      "slow": 26,
      "sides": ["buy"]
    }
  },
  "notify": {
    "telegram": {
      "enabled": false,
      "chatId": "",
      "secretRef": "conn/telegram/botToken"
    }
  }
}
```

Check without network and keys:

```bash
vend config validate
```

What matters in these values:

- The manual request from step 3 uses `timeframe: 1m`. With `horizonBars: 3`
  the horizon is 3 minutes: the labeler waits `(3 + 1) × 1m` = 4 minutes after
  the verdict, so the label appears in 5–6 minutes including the 30 second
  cycle.
- `timeframe: 5m` for trend is a separate schedule: the scan runs 2 seconds
  after each 5m bar closes, not on the labeling timer.
- `labelIntervalSec: 30` — the labeling, notification and status-file cycle;
  it does not affect the trend schedule.
- `feeBps: 10` — per side; `netReturn` subtracts 20 bps round-trip.
- `sides: ["buy"]` — buys only; trend emits an entry at the EMA fast/slow
  crossing on a closed bar.
- `venue: "binance"` — the instrument venue in the signals; the value is
  checked by validation.
- Telegram can be enabled (`enabled: true`, `chatId`, `secretRef`); then
  `doctor --live` also sends one test message. It is not required for the quick
  gate check.

## Step 3. Start the shadow cycle (6 minutes of waiting)

```bash
vend serve --addr 127.0.0.1:8420
```

`starting on http://127.0.0.1:8420` appears on stderr. Do not close the
process: the runner works inside `serve`. The first cycle runs immediately,
then every 30 seconds.

Right after the start, send one manual request with `timeframe: "1m"` — this is
a mandatory step of the check. Trend on 5m may not produce a crossing in a calm
market, while a manual 1m request guarantees a label in 5–6 minutes. Example
(substitute your token through the environment variable and the current UTC
time):

```bash
curl -sS -X POST http://127.0.0.1:8420/v1/gate \
  -H "Authorization: Bearer $VENDING_SECRET_CONN_LOCAL_APITOKEN" \
  -H "Content-Type: application/json" \
  --data '{"signalId":"smoke-1m-1","instrument":{"venue":"binance","symbol":"BTCUSDT"},"side":"buy","intentKind":"entry","timeframe":"1m","ts":"<now UTC RFC3339>","price":"100000","source":"smoke"}'
```

In PowerShell 5.1 use `curl.exe`; the token is
`$env:VENDING_SECRET_CONN_LOCAL_APITOKEN`. The `gate.verdict` response means
the signal was accepted and written to the journal; the label appears by
itself, there is no need to repeat the request.

## Step 4. What to look at after 5–6 minutes

### `vend status --json`

```bash
vend status --json
```

Fields: `version`, `policyHash`, `events`, `labeled`, `lastLabelAt`,
`lastCycleError`, `updatedAt`, `lastSeq`. `lastLabelAt`, `lastCycleError` and
`lastSeq` are omitted when empty.

Expected:

- `events` > 0 — the journal has `gate.request`, `decision.response`,
  `gate.verdict` (3 events per signal, plus labels and notifications);
- `labeled` ≥ 1 — the label of the manual 1m request: the horizon of 3 bars
  plus one spare = 4 minutes after the verdict, plus the 30 second cycle. If
  there is no label after 8 minutes, see "If something is wrong";
- `lastLabelAt` — the time of the last label; after step 3 it must not be
  empty;
- `policyHash` — 64 hex, the same as `vend doctor` prints;
- `lastCycleError` empty; if there is text, the cycle failed, see "If something
  is wrong";
- `updatedAt` no older than `labelIntervalSec` plus a few seconds.

Without a single `serve` run there is no status file: `events`/`labeled` are
counted from the journal, `policy: none`, `last label: never`. After a stop the
file remains, so `updatedAt` stops being updated; check freshness while `serve`
runs.

### `vend gate report`

```bash
vend gate report
```

After 5–6 minutes expected:

```text
assumptions: horizon=3 bars, fees=20 bps round-trip, price source=marketdata-binance, equal size, no slippage, no funding
verdicts: ALLOW=<n> REJECT=<n> ABSTAIN=<n> SKIP=<n>
window: days=<n> active=<n>
labeled: <n>
awaiting labeling: <n>
no outcome by deadline: 0
source rejected: 0
policy: <12 hex>
other policies: 0
backfill: labeled=0 excluded=0
decision cost: <decimal> USD
latency p50/p95: <n>ms/<n>ms
insufficient data: <n> labeled, need 100
```

- `verdicts` counts unique decisions by the latest verdict per `decisionId`;
  the journal has one `gate.request`/`decision.response`/`gate.verdict` per
  signal, so `events` in the status is larger than the sum of verdicts.
- `window` — `days` by the `gate.request` receipt time (UTC), not by the `ts`
  from the signal body; `active` — the number of distinct UTC dates with at
  least one request.
- `labeled` — the number of labels attached to decisions of the selected
  policy; `backfill` — late signals (by default only counted, not compared).
- `awaiting labeling` — entry signals without a label and without
  `outcome.unlabelable`; `no outcome by deadline` — signals for which no honest
  window was found after 7 days and an `outcome.unlabelable` with reason
  `timeout` was written; `source rejected` — signals for which the source
  rejected the request as `invalid_request` (for example, the pair is not on
  Binance Spot) and an `outcome.unlabelable` with reason `source_rejected` was
  written.
- `decision cost` — the sum of `usage.costUsd` of unique decisions;
  `latency p50/p95` — by `usage.latencyMs`.
- `insufficient data` below 100 labels is **normal**, not an error. Comparisons
  (groups, bootstrap CI, Brier/BSS/AUC/ECE, per-model breakdown) appear only
  from 100 labels; BSS is printed as `bss: n/a` when the base rate is 0 or 1.
- Comparisons mix only the latest `(horizonBars, feeBps)` pair; labels with
  another pair are not mixed in (in `--json` this is `otherLabelParams`).

`entryLagMedian`, `entryLagP95` (in fractions of one bar) and `entryShifted`
are part of the report assumptions and are printed in `--share`; the
human-readable `gate report` does not show them. `entryLagMs` is the delay from
the opening of the entry bar to the moment the signal became available (the
verdict time, otherwise the receipt time). The entry shifts to the next bar if
the bar opened earlier than the signal availability by more than
`min(10% of the interval, 30 seconds)`; without a spare bar the signal waits for
the next cycle and writes no outcome without the shift. In `--share`, the same
value in seconds is printed next to the bar fractions.

### `vend gate report --share`

```bash
vend gate report --share
```

Prints an anonymized summary: version, policy prefix (8 hex), number of
decisions, `days` and `active days` by `gate.request` receipt time in UTC,
number of labels, number awaiting labeling, expired without an outcome and
rejected by the source (`awaiting labeling`, `no outcome by deadline`,
`source rejected`), verdict counters, metrics or `insufficient data`,
assumptions (horizon, round-trip fee, entry lag median/p95 in bar fractions and
in seconds, entry shifted, equal size/no slippage/no funding) and a link to
GitHub Discussions. It contains no symbols, venues, prices, state, paths,
identifiers or return amounts. A file written through `--out <file>` gets
permissions 0600.

## Step 5. What to send for review

Collect four files (any names). `doctor.txt` was already saved in step 1 — a
second paid `doctor --live` call is not needed:

```bash
vend status --json > status.json
vend gate report --share > share.md
vend journal verify > verify.txt 2>&1
```

Send:

1. `doctor.txt` — the output of the live probes and codes (without tokens:
   doctor does not print them);
2. `status.json` — counters, policy hash, the last cycle error;
3. `share.md` — the anonymized summary (look it over before sending);
4. `verify.txt` — the `journal verify` result (must be `ok`, the event count).

Do not send: the config, environment variables, the `--save-raw` file,
`vending.db`, screenshots with keys. If `doctor` or `serve` prints a path or a
token — cut it out.

## Rehearsal without network (smokestub)

`tools/smokestub` is a local stub of the three providers for rehearsing this
runbook without keys and network. It answers `POST /v1/systemone` as
decision-drex, serves synthetic closed BTCUSDT candles on `1m`, `5m`, `1h` at
`GET /api/v3/klines` and confirms Telegram `sendMessage`. The stub is not part
of `tools/release`, listens on loopback only and must not be used outside the
rehearsal. `GET /ctl/stats` returns request counters by role —
`{"decision":N,"telegram":N,"klines":{"BTCUSDT":N}}`; tokens and message texts
never reach the counters or the log. The automated end-to-end test `tools/e2e`
uses these counters.

1. Start the stub in a separate terminal:

   ```bash
   go run ./tools/smokestub --addr 127.0.0.1:28471
   ```

   Addresses other than `127.0.0.1` and `::1` are rejected at startup.

2. Made-up secrets and config. The values below are rehearsal-only strings, not
   real keys:

   ```bash
   export VENDING_SECRET_CONN_DREX_APIKEY='rehearsal-only'
   export VENDING_SECRET_CONN_TELEGRAM_BOTTOKEN='rehearsal-only'
   export VENDING_SECRET_CONN_LOCAL_APITOKEN='rehearsal-only'
   ```

   Config fragment (the other keys stay as `init` created them):

   ```json
   {
     "mode": "shadow",
     "outcome": { "horizonBars": 3, "feeBps": 10, "labelIntervalSec": 30 },
     "providers": {
       "decision": {
         "id": "decision-drex",
         "model": "drex-max-v1.1",
         "profile": "drex",
         "baseUrl": "http://127.0.0.1:28471",
         "secretRef": "conn/drex/apiKey"
       },
       "marketdata": {
         "id": "marketdata-binance",
         "baseUrl": "http://127.0.0.1:28471"
       }
     },
     "signals": {
       "trend": {
         "enabled": true,
         "venue": "binance",
         "symbols": ["BTCUSDT"],
         "timeframe": "5m",
         "fast": 12,
         "slow": 26,
         "sides": ["buy"]
       }
     },
     "notify": {
       "telegram": {
         "enabled": true,
         "chatId": "1000001",
         "secretRef": "conn/telegram/botToken",
         "baseUrl": "http://127.0.0.1:28471"
       }
     }
   }
   ```

3. Go through the same steps 1–5 as for the live check:
   `vend config validate`, `vend doctor --live --symbol BTCUSDT`,
   `vend serve --addr 127.0.0.1:8420`, a manual `POST /v1/gate` with
   `timeframe: "1m"` and `price: "100000"`, then after 5–6 minutes
   `vend status --json`, `vend gate report`, `vend gate report --share` and
   `vend journal verify`. All `doctor --live` probes must be `ok`, and the
   report must print `source rejected: 0`.

4. Rehearse failures (optional). Switch the role HTTP statuses:

   ```bash
   curl -s "http://127.0.0.1:28471/ctl?decision=500&telegram=401"
   ```

   A new manual signal gets `ABSTAIN` with the code `provider_unavailable`, and
   Telegram gets a delivery error. Return the roles to normal:

   ```bash
   curl -s "http://127.0.0.1:28471/ctl?decision=200&telegram=200"
   ```

   The stub does not write the bot token or the message text to the log: only
   the address and the `/ctl` switches. Check which calls it received:

   ```bash
   curl -s "http://127.0.0.1:28471/ctl/stats"
   ```

   The response contains the number of decision and Telegram requests and
   klines by symbol. After one manual 1m signal and its label, one decision
   request and one klines request for `BTCUSDT` are expected.

## If something is wrong

| Symptom | Cause and action |
|---|---|
| `doctor` returns 1 | look at the `FAIL <check>` line: config, directory, DB, journal, providers |
| `decision: FAIL verdict=ABSTAIN code=invalid_output` | the live answer did not pass the gate question check; retry, and on a repeat send `--save-raw` |
| `decision: auth` | the key is missing/wrong or the quota is exhausted; check `VENDING_SECRET_CONN_DREX_APIKEY` |
| `candles: invalid_request` | the symbol/interval is not accepted by Binance; check `--symbol BTCUSDT` |
| `candles: ip_banned` | the IP is banned; do not retry, change the network |
| `events: 0` after 5 minutes | `serve` is not running or looks at another `--data-dir` |
| `labeled: 0` after 8 minutes | no manual 1m request, not enough closed bars, no network or a wrong `--data-dir`; check `lastCycleError` |
| `verdicts: ABSTAIN=<n>` | the provider failed; the cause code is in `gate.verdict` (`provider_auth`, `provider_unavailable`, `invalid_output`, `provider_misconfigured`) |
| `lastCycleError` is not empty | the cycle keeps running; the error repeats every interval — include the text in the report |
| `journal chain broken at seq N` | the journal is damaged; do not continue, save the file and report |

The broader first-run table, with the actual messages and exit codes, is in
[troubleshooting](troubleshooting.md).

Recent notifications without stopping `serve`:

```bash
vend journal tail --kind notify.sent --limit 20
```

A line is `seq ts kind decisionId`, without the payload. `--json` shows the
payload (including `ok` and `uptoSeq`): `ok: false` means a failed send. The
command only reads the journal and does not verify the chain — use
`vend journal verify` for that.

## Result of the check

Record: date/time (UTC), version, policy prefix, the number of events, labels
and verdicts, the `doctor --live` codes, the `journal verify` result. This data
is the input to G1 and to the decision about the next iteration of the wedge.
