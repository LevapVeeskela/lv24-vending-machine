# LV24 Vending

[Русский](README.ru.md)

LV24 Vending is a local shadow gate for trading signals. Your bot sends an
entry signal to the loopback API, the gate asks a decision model for a second
opinion, and the verdict is written to a hash-chained journal. Later the gate
labels the entry from closed candles and reports whether filtering the signal
would have helped or hurt. It never places an order.

## Who it is for

- You already run a bot such as [Freqtrade](docs/recipes/freqtrade.md) and want
  an independent check before an entry.
- Or you enable the built-in `signals.trend` source (EMA fast/slow on closed
  candles) and let it produce the entry signals.

## What you need

- A decision-provider key for `decision-drex` (Drex or Jev profile). The key is
  yours (BYOK); the binary calls your provider directly.
- Candles: the default `marketdata-binance` provider reads the public Binance
  Spot klines endpoint and needs no key.
- Go 1.27 to build from source. The `0.1.0-m1` binaries are not published yet.

## Quick start

Offline, no key, no network:

```bash
go build -o vend ./core/cmd/vend
./vend init
./vend gate test --request contracts/examples/gate-request.json \
  --fixture core/cmd/vend/testdata/decision-response.json
./vend gate report
```

On Windows build `vend.exe` and call `.\vend.exe`:

```powershell
go build -o vend.exe ./core/cmd/vend
.\vend.exe init
.\vend.exe gate test --request contracts/examples/gate-request.json --fixture core/cmd/vend/testdata/decision-response.json
.\vend.exe gate report
```

`init` creates the config and the SQLite journal; `gate test` replays a
recorded decision response through the same flow as a live call; `gate report`
prints the counters. The full paths:

- [Quickstart](docs/quickstart.md) — 15 minutes with a real decision key, or
  the offline path above.
- [Install](docs/install.md) — prebuilt binary or source build, checksums and
  the unsigned-binary warnings.
- [Live smoke runbook](docs/runbook-live-smoke.md) — 15-minute live check with
  `doctor --live`, one manual 1m signal and one label.
- [Seven days of operation](docs/run-7-days.md) — the multi-day shadow run and
  the G1 criterion.
- [Troubleshooting](docs/troubleshooting.md) — the first-run messages, their
  causes and what to do.

## What you get

`vend gate report` prints counters and, once there are enough labels, return
comparisons. Model quality is reported only with at least 30 calibration pairs;
below that every metric is `n/a`, not zero. Before 100 labels the report says so
instead of inventing a result:

```text
verdicts: ALLOW=<n> REJECT=<n> ABSTAIN=<n> SKIP=<n>
window: days=<n> active=<n>
labeled: <n>
awaiting labeling: <n>
insufficient data: <n> labeled, need 100
```

`vend gate report --share` prints the same picture as a sanitized Markdown
summary: aggregate counts, metrics when available, assumptions, and the
aggregate ALLOW-vs-REJECT mean return difference with its 95% interval. It
contains no symbols, venues, prices, position sizes, data paths, state or
signal/decision ids. It is meant to be posted manually.

The report describes what the filter would have done with the signals it saw.
It is not a profit forecast and not investment advice; a positive sample says
nothing about the next trade.

## Status and limitations

- **Prerelease.** `0.1.0-m1` is a release candidate and is not published.
- **Unsigned binaries.** Build artifacts are not code-signed or notarized;
  `SHA256SUMS` is the only integrity check. Read
  [install](docs/install.md#unsigned-binaries) before running one.
- **Shadow only.** The gate evaluates and journals; there is no order execution
  path. `live` is rejected by config validation; `paper` and `sandbox` are
  accepted by the config but have no OMS behind them yet.
- **Not done yet:** release publication, TradingView webhook adapter,
  desktop/web/mobile clients, paper/live execution, two-way Telegram bot.
- **Verified environment.** The offline end-to-end rehearsal against the local
  stub was run on Windows 11. On Linux, `vend_linux_amd64` was checked under WSL
  Ubuntu with `version`, `init` and `doctor`; the rest of the Linux path was not
  run. macOS was not run at all. The live path with real decision, candle and
  Telegram providers has not been exercised on any platform yet.

## Security

- Secrets are read from `VENDING_SECRET_*` environment variables and referenced
  by `secretRef`; they are never written to the config, the journal or the logs.
- The HTTP API binds `127.0.0.1` and requires a bearer token.
- The journal is append-only with a hash chain (`vend journal verify`).
- `--share` is sanitized on purpose; `--save-raw` may contain vendor payloads
  and must not be published.
- There is no guaranteed return and no investment recommendation. See
  [SECURITY.md](SECURITY.md).

## Contributing

Contributions are accepted under Apache-2.0 and there is no CLA; see
[CONTRIBUTING.md](CONTRIBUTING.md) for the checks and the hard rules.

## License

Code, providers and SDKs are **Apache-2.0** (see [LICENSE](LICENSE) and
[NOTICE](NOTICE)); documentation is CC-BY-4.0 (see
[LICENSE-docs](LICENSE-docs)). A commercial mobile app can be distributed
through the App Store, Google Play and RuStore — Apache-2.0 allows it.
AGPL/GPL dependencies are forbidden in the core and the mobile build; see
[ADR-0007](docs/adr/0007-license-and-stores.md) and
[ADR-0012](docs/adr/0012-license-apache-2.md).

## Target platform (not implemented)

The target is a deterministic Go core (`vend`, later `vendd`) with a Tauri +
React desktop app, a React web client and a Flutter mobile app, plus brokers,
paper/live execution and the OMS. Only the core and the CLI exist today; the
other clients and the execution path are not implemented.
