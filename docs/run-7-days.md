# Seven days of continuous shadow-gate operation

[Русская версия](run-7-days.ru.md)

Goal: accumulate data for criterion G1 — **≥ 7 days of operation and ≥ 100 labeled
decisions** (`docs/spec/10-roadmap.md`). M1 runs only in `shadow`: no orders are
sent, no money moves. Live provider calls use only the owner's keys.

This document is about a long run, not a quick check. The 15-minute quick check
is [runbook-live-smoke.md](runbook-live-smoke.md). If the installation does not
exist yet, `vend init --quick` writes the G1 profile (ten USDT symbols on `15m`,
both sides) and prints the two secret exports — see
[quickstart.md](quickstart.md). A plain `init` leaves `signals.trend` off and
produces no signals on its own.

## 1. Why `vend serve` in a terminal and not a service

Today secrets are read only from the `VENDING_SECRET_*` environment variables
(see [quickstart.md](quickstart.md), step 3). M1 has no secrets file and no
OS keychain.

`systemd`, `launchd` and the Windows scheduler are **not recommended** while
secrets would have to live in a file: `Environment=` in a unit file,
`EnvironmentVariables` in a plist or variables in a script are the same secret
on disk. There are no ready-made unit files in the repository.

The primary way is therefore a persistent terminal session:

- Linux / macOS: `tmux` or `screen`;
- Windows: a separate PowerShell window.

The computer must not go to sleep: sleep stops both the HTTP server and the
background runner. Configure sleep in the OS manually (Windows: Settings →
System → Power & battery → Screen and sleep; macOS: System Settings → Energy;
Linux: desktop settings or
`systemd-inhibit --what=sleep:idle --why="vend shadow gate" vend serve ...`).
Restore the previous settings after the week.

Running in `tmux` (Linux/macOS):

```bash
tmux new -s vend
export VENDING_SECRET_CONN_DREX_APIKEY='<token>'
export VENDING_SECRET_CONN_LOCAL_APITOKEN='<token>'
# when Telegram is enabled:
export VENDING_SECRET_CONN_TELEGRAM_BOTTOKEN='<token>'
vend serve --addr 127.0.0.1:8420
# detach: Ctrl+B, then D; reattach: tmux attach -t vend
```

Windows, a separate PowerShell window:

```powershell
$env:VENDING_SECRET_CONN_DREX_APIKEY = '<token>'
$env:VENDING_SECRET_CONN_LOCAL_APITOKEN = '<token>'
.\vend.exe serve --addr 127.0.0.1:8420
```

Do not close the window. `serve` listens on loopback only and starts the
background runner: labeling, trend signals, notifications and writing
`shadow-status.json`.

## 2. Restart and stop

The journal lives in the data directory (`vending.db`, `shadow-status.json`),
the config separately (`vend config path`). After a reboot or `Ctrl+C`, set the
environment variables again and start `vend serve` with the same `--config` /
`--data-dir` (or the same `VENDING_CONFIG_FILE` / `VENDING_DATA_DIR`).

- the journal continues from the last event; the chain does not start over;
- `lastLabelAt` is restored from the last `outcome.labeled` when the runner
  starts;
- delivered notifications are not repeated: the alert state is derived from the
  journal (`notify.sent` with `uptoSeq`), so a quiet journal stays quiet.

Check against `tools/smokestub` (a local stub, no network and no real keys;
Windows, 2026-11-16):

```bash
go build -o vend ./core/cmd/vend
go run ./tools/smokestub --addr 127.0.0.1:28471   # separate terminal
# in the config: providers.*.baseUrl = http://127.0.0.1:28471,
# made-up VENDING_SECRET_* and notify.telegram with the stub baseUrl
vend serve --addr 127.0.0.1:8420
```

Before the stop:

```text
events: 14
labeled: 1
lastLabelAt: 2026-11-16T20:15:52.9232211Z
notify.sent: 1 (provider_unavailable, uptoSeq=13)
```

After stopping `serve` and restarting it with the same variables:

```text
events: 14
labeled: 1
lastLabelAt: 2026-11-16T20:15:52.9232211Z
notify.sent: 1
journal verify: journal ok: 14 events
```

The event counter did not grow, the label is the same, no `notify.sent` was
repeated. Check commands:

```bash
vend status --json
vend journal verify
```

## 3. Daily check

Once a day, without stopping `serve`:

```bash
vend status --json
vend journal verify
```

In `status --json`:

- `updatedAt` — the time of the last status write; it must be no older than
  `outcome.labelIntervalSec` plus a few seconds (300 seconds by default);
- `labeled` — the number of labels; it must grow;
- `g1` — the G1 progress block; `lastLabelAgeHours` must stay below
  `expectedLabelIntervalHours` and `labelsDue` must stay at zero (section 4);
- `lastCycleError` — absent or empty; any text means a cycle error;
- `lastLabelAt` — the time of the last label;
- `policyHash` — 64 hex, the same as `vend doctor` prints.

`journal verify` must print `journal ok: N events`. A broken chain means: stop,
save the data directory and report it; do not continue writing.

If `serve` is stopped, `updatedAt` in the file stays old: freshness is checked
only while the process runs.

## 4. G1 progress: labels, days, gap

`vend status` and `vend gate report` print the same block, derived from the
journal and never from `shadow-status.json`:

```text
g1: labels 23/100, active days 4/7, max gap 9.5h, eligible=no
last label age: 0.6h
expected label interval: 3.25h
labels due: 1
```

The same numbers are in JSON, under `g1`:

```json
"g1": {
  "labels": 23,
  "targetLabels": 100,
  "activeDays": 4,
  "targetDays": 7,
  "maxGapHours": 9.5,
  "eligible": false,
  "synthetic": false,
  "lastLabelAgeHours": 0.6,
  "expectedLabelIntervalHours": 3.25,
  "labelsDue": 1
}
```

On Windows PowerShell:

```powershell
(.\vend.exe status --json | ConvertFrom-Json).g1
```

With `jq`: `vend status --json | jq .g1`.

In JSON, `lastLabelAgeHours: -1` means no label yet (`never` in the human
view); `labelsDue: -1` means the window is unknown (`unknown`).

| Field | Meaning |
|---|---|
| `labels N/100` | labeled outcomes of the audited policy. Backfilled labels are never counted, even with `--include-backfill`, so with that flag this number can be lower than `labeled:`. |
| `active days D/7` | distinct UTC dates with a `gate.verdict`. ABSTAIN counts: the gate answered. A date with only `gate.request` does not count. |
| `max gap Hh` | the longest pause between two consecutive verdict timestamps. It is measured between answers, not between dates, so seven dates with a three-day hole cannot pass. |
| `eligible=yes/no` | yes only when `labels >= 100`, `active days >= 7`, `max gap <= 36h` and the journal is not synthetic. |
| `last label age` | hours since the latest `outcome.labeled`. `never` means there is no label yet. |
| `expected label interval` | `(horizonBars + 1) × timeframe`: 13h for the defaults (`12`, `1h`), 3.25h for the quick profile (`12`, `15m`). `n/a` when the journal has no labeled outcome and no config was passed. |
| `labels due` | entry signals whose labeling window has already expired and that have neither `outcome.labeled` nor `outcome.unlabelable`. `unknown` means the window cannot be derived. |

`window: days=N active=M` is a different pair of numbers: `active` counts UTC
dates with at least one accepted `gate.request`, while the G1 `active days`
counts dates with an answer. A week of requests that the gate never answered is
not a week of work.

### What counts as a failure

- **Fewer than 100 labels at the end of day seven.** The last decisions of day
  seven cannot be labeled before the week ends: a label needs
  `(horizonBars + 1)` bars after the verdict. Keep the gate running until the
  counter reaches 100; the calendar week alone is not enough.
- **Fewer than 7 active days.** The gate must answer on seven different UTC
  dates, not merely hold a journal for seven dates.
- **A gap above 36 hours.** `eligible` stays `no` even with 100 labels and seven
  dates; the week was not continuous.
- **`eligible=no` for any of the reasons above, or a synthetic journal.**
- **`labels due` above zero and growing.** That many entry signals passed their
  labeling window without a label or an `unlabelable` record: the gate answers
  but the labeler is stuck (candles unavailable, `lastCycleError` non-empty, a
  symbol without history). Compare with `awaiting labeling:` in the report.
- **`last label age` above `expected label interval`.** Labels stopped even if
  the gate still answers. Check `lastCycleError`, `providers.marketdata.baseUrl`
  and the network, then `vend journal tail --kind outcome.labeled` and
  `--kind outcome.unlabelable`. The full table is in
  [troubleshooting.md](troubleshooting.md).
- **`last label age: never`.** Normal before the first window elapses; a failure
  only after `expected label interval` plus a runner cycle with no label.

### What is not counted

- **`vend demo`.** Any journal that contains a `runId=demo` event is synthetic:
  the G1 counters are zeroed (`g1: 0/100, 0/7`), the block prints
  `SYNTHETIC — not G1 evidence`, `eligible` is false and `gate report --share`
  exits 2 with `synthetic journal is not G1 evidence`. There is no flag that
  admits a demo journal into G1. Do not reuse a demo directory for a real run:
  the marker is permanent in that journal.
- **Backfilled labels.** A signal that arrived more than one timeframe after its
  `ts` is `backfill`; its label is excluded from the counts and comparisons by
  default and the report prints `backfill: labeled=N excluded=M`.
  `--include-backfill` admits those labels into the return comparisons, but
  never into G1: the G1 label count ignores backfills at any flag.
- **The offline fixture and replays.** `gate test` proves the wiring, not market
  behaviour: a fixture has no candles behind it, so it produces no labels, and a
  replay produces no new decision.

## 5. Volume target

100 labels in 7 days is on average **100 / 7 ≈ 15 labels per day**.

Before the week, check how many signals your `signals.trend` config produces:

```bash
vend signals estimate --days 7
```

The command reads closed candles of the configured marketdata provider for the
last 7 days plus the EMA warm-up, counts crossings with the same code as the
live scan, and prints one line per symbol, a total and a verdict. It writes
nothing to the journal, does not call the decision provider or Telegram, and
needs no secrets.

A `SYMBOL error=invalid_request` line means the symbol history is shorter than
the window (for example, the pair was listed recently): reduce `--days` or
remove the symbol.

Aim for the line `verdict: enough`. If there are too few signals:

- add symbols to `signals.trend.symbols` (each symbol is its own signal
  stream);
- or reduce the `signals.trend.timeframe` (for example, `1h` → `15m`): a
  smaller timeframe has more bars and crossings;
- or connect your own signal source through `POST /v1/gate`.

### Real-market measurement

The owner measured the signal frequency with `vend signals estimate --days 7`,
the release binary `0.1.0-m1` and Binance candles on 2026-11-17 16:44 UTC.
This is one week of one market; past frequency does not predict future
frequency.

| symbols | timeframe | sides | signals per day | verdict |
|---|---|---|---|---|
| BTCUSDT | 1h | buy | 0.6 | low |
| BTCUSDT | 5m | buy | 4.9 | low |
| 5 symbols | 1h | buy | 1.9 | low |
| 5 symbols | 15m | buy | 11.4 | low |
| 5 symbols | 5m | buy | 27.7 | enough |
| 5 symbols | 15m | buy,sell | 22.9 | enough |
| 10 symbols | 1h | buy,sell | 8.3 | low |
| 10 symbols | 15m | buy | 21.7 | enough |

5 symbols = BTCUSDT, ETHUSDT, SOLUSDT, BNBUSDT, XRPUSDT. 10 symbols = the same
plus DOGEUSDT, ADAUSDT, LINKUSDT, AVAXUSDT, LTCUSDT.

A single symbol on `1h` does not reach the threshold. For the start of G1 the
config must be at the level of "10 symbols on `15m`" or "5 symbols on `5m`",
and it must be checked with `vend signals estimate --days 7` before the week
starts.

Every signal is one decision-provider call, so the number of calls grows with
the number of signals: about 150 calls per week at 22 signals per day. The
cost of one call is not stated in this document — unknown.

Example of the output format only, against `tools/smokestub` (Windows,
2026-11-16; **the candles are synthetic**): it shows the format, not market
volume.

```text
BTCUSDT signals=8 per_day=1.1
total: signals=8 per_day=1.1
target: 15 per day (100 labeled in 7 days)
verdict: low
past frequency does not predict future frequency
```

During the first day compare the number of **decisions**, not labels: the
`verdicts` line in `vend gate report`. A label appears `(horizonBars + 1)` bars
after the verdict — with the default values that is `(12 + 1) × 1h = 13` hours —
so in the first hours there are necessarily fewer labels than decisions.

The actual `signals.trend` frequency depends on the market: an EMA fast/slow
crossing on the chosen timeframe may not happen even once a day. There is no
guaranteed number of signals per day; 15 is a target, not a promise.

Measurement of one runner cycle on a 50,000-event journal (`go -C core test ./internal/app/shadow -run '^$' -bench BenchmarkRunnerCycle50kEvents -benchtime=1x`): 1.34 s — **a measurement on one machine, not a guarantee** (Windows 11, Intel Core Ultra 9 185H, 2026-11-17).

## 6. What to send on day seven

Only an anonymized summary:

```bash
vend gate report --share
```

It contains no symbols, venues, prices, position sizes, paths or identifiers;
the aggregate ALLOW-vs-REJECT mean return difference and its 95% interval are
included. It also carries the G1 lines (`g1 labels`, `g1 active days`,
`g1 max gap hours`, `g1 eligible`, `g1 labels due`, `g1 last label age hours`,
`g1 expected label interval hours`). A synthetic journal is refused with exit 2
and `synthetic journal is not G1 evidence`. The summary is published manually.

**Do not send:** the config, keys or environment variables, `vending.db` (the
journal), `shadow-status.json` with paths, screenshots with keys.

## 7. What has not been verified on this machine

The commands and the runner-cycle measurement in this document were checked on
Windows 11. On Linux, only `version`, `init` and `doctor` were run on
`vend_linux_amd64` under WSL Ubuntu; the seven-day run, service supervision and
`tmux` have not been verified there. macOS was not run at all. A real seven-day
run with live Drex, Binance and Telegram has not been performed either — only
the rehearsal against `tools/smokestub`. The signal frequency was measured once
over one week (section 5); a full week of operation was not repeated and its
stability is unknown.
