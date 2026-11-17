# Seven days of continuous shadow-gate operation

[Русская версия](run-7-days.ru.md)

Goal: accumulate data for criterion G1 — **≥ 7 days of operation and ≥ 100 labeled
decisions** (`docs/spec/10-roadmap.md`). M1 runs only in `shadow`: no orders are
sent, no money moves. Live provider calls use only the owner's keys.

This document is about a long run, not a quick check. The 15-minute quick check
is [runbook-live-smoke.md](runbook-live-smoke.md).

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
- `lastCycleError` — absent or empty; any text means a cycle error;
- `lastLabelAt` — the time of the last label;
- `policyHash` — 64 hex, the same as `vend doctor` prints.

`journal verify` must print `journal ok: N events`. A broken chain means: stop,
save the data directory and report it; do not continue writing.

If `serve` is stopped, `updatedAt` in the file stays old: freshness is checked
only while the process runs.

## 4. Volume target

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

## 5. What to send on day seven

Only an anonymized summary:

```bash
vend gate report --share
```

It contains no symbols, venues, prices, position sizes, paths or identifiers;
the aggregate ALLOW-vs-REJECT mean return difference and its 95% interval are
included. It is published manually. Criterion G1 is checked by the `active days`
and `labeled outcomes` lines.

**Do not send:** the config, keys or environment variables, `vending.db` (the
journal), `shadow-status.json` with paths, screenshots with keys.

## 6. What has not been verified on this machine

The commands and the runner-cycle measurement in this document were checked on
Windows 11. On Linux, only `version`, `init` and `doctor` were run on
`vend_linux_amd64` under WSL Ubuntu; the seven-day run, service supervision and
`tmux` have not been verified there. macOS was not run at all. A real seven-day
run with live Drex, Binance and Telegram has not been performed either — only
the rehearsal against `tools/smokestub`. The signal frequency was measured once
over one week (section 4); a full week of operation was not repeated and its
stability is unknown.
