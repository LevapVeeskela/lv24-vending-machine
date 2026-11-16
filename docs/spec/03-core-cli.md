# 03. Ядро и CLI

Статус: draft v0.1 · Зависит от: `01-architecture.md`, `02-monorepo.md`

## 1. Два бинарника

| Бинарник | Роль | Особенности |
|---|---|---|
| `vend` | CLI, «пульт» | Мгновенный старт, `--json`, скриптуемость, офлайн-команды |
| `vendd` | демон/сервер | Event loop, провайдеры, API, планировщик, OMS |

`vend` не содержит торговой логики: он либо работает офлайн (конфиг, схемы,
бэктест), либо обращается к локальному API `vendd`. Это даёт одинаковое поведение
desktop/web/mobile и CLI — все ходят в один контракт.

Жизненный цикл:

```
vend run start            # если vendd не запущен — auto-start (detached) + hint
vend daemon status|start|stop|restart
vendd                     # foreground, логи в stderr
vendd --config path --addr 127.0.0.1:8420
```

Состояние: `~/.local/share/lv24-vending/` (`%LOCALAPPDATA%` на Windows, `~/Library/
Application Support` на macOS): `vending.db`, `run/` (pid, unix-socket), `logs/`,
`cache/`, `packs/`.

## 2. Команды

### 2.1 Онбординг

```bash
vend init                       # интерактивно: режим, биржа, ключи, агенты
vend init --template crypto-paper --non-interactive
vend doctor                     # окружение, БД, часы, сеть, провайдеры, ключи
vend doctor --json
vend version [--json]
vend update [--check]
vend completion powershell      # bash|zsh|fish|powershell
```

### 2.2 Провайдеры

```bash
vend provider catalog [--kind venue|marketdata|llm|decision|notifier]
vend provider show exchange-binance          # capabilities + config schema
vend provider connect exchange-binance --id binance-main --mode paper
vend provider test binance-main              # ключи, права, время сервера, rate limit
vend provider health [--watch]
vend provider remove binance-main
```

`provider show` выводит capability-матрицу человекочитаемо:

```
exchange-binance v1.4.0  (venue, marketdata)
  asset classes  spot, margin, futures
  order types    market, limit, stop, stop_limit, trailing, oco
  tif            gtc, ioc, fok, day, gtd
  features       post_only, reduce_only, user_data_stream, sandbox, batch
  rate limits    orders: 50/10s, market: 1200/min
  degradation    trailing_stop -> emulated by core (explicit)
```

### 2.3 Рынок

```bash
vend market instruments --venue binance-main --query BTC
vend market quote BTC/USDT
vend market candles BTC/USDT --tf 1h --limit 200 [--json|--csv]
vend market orderbook BTC/USDT --depth 20
vend market stream BTC/USDT --tf 1m [--jsonl]
```

`--jsonl` — поток строк (для пайпов), `--csv` — для таблиц/Excel.

### 2.4 Счёт и ордера

```bash
vend account list
vend account balances --account binance-main
vend account positions --account binance-main
vend account orders --open
vend account pnl --period 30d [--by instrument|strategy|agent]

vend order place --account binance-main --symbol BTC/USDT \
    --side buy --type limit --qty 0.01 --price 60000 --tif gtc --post-only
vend order place --json @order.json
vend order cancel <order-id|client-id>
vend order cancel-all --account binance-main
vend order show <id> --events
```

Ордер всегда проходит RiskGate. Если нужен approve:
`exit 9` + JSON `{ "status": "needs_approval", "approvalId": "..." }`.

### 2.5 Стратегии и запуски

```bash
vend strategy list [--builtin]
vend strategy new my-grid --from grid
vend strategy validate ./packs/my-grid
vend run start grid-btc --account binance-main --symbol BTC/USDT --mode paper
vend run list
vend run inspect <run-id>        # состояние, позиция, P&L, последние решения
vend run pause|resume|stop <run-id>
vend run logs <run-id> --follow
```

### 2.6 Агенты

```bash
vend agent list                       # роли, модели, бюджеты, статус
vend agent show analyst
vend agent test analyst --input examples/market-snapshot.json
vend agent run analyst --run <run-id> --once
vend agent explain <decision-id>      # полный trace: вход, модель, риск, исход
vend agent budget [--set dailyUsd=10]
vend agent matrix                     # кто за что отвечает (роль x вендор)
```

`vend agent matrix` — ключевая команда для «гибкой настройки, какой агент за что
отвечает»: таблица `role × venue × instrument-class → agent/model`, с подсветкой
пробелов и конфликтов.

### 2.7 Риск

```bash
vend risk policy list|show default
vend risk policy validate ./policies/strict.yaml
vend risk policy apply strict --account binance-main
vend risk simulate --proposal proposal.json --account binance-main
vend risk kill on [--reason "manual"] [--cancel-open]
vend risk kill off
vend risk status
vend approve list
vend approve accept <id> [--note "..."]
vend approve reject <id> --reason "..."
```

### 2.8 Бэктест

```bash
vend backtest run grid-btc --from 2025-01-01 --to 2026-01-01 \
    --symbol BTC/USDT --tf 1h --cash 10000 --fees 0.001 --slippage bps:5
vend backtest run grid-btc --data ./testdata/btc-1h.parquet --deterministic
vend backtest report <id> --metrics sharpe,drawdown,winrate --equity-curve
vend backtest compare <id-a> <id-b>
vend backtest walkforward grid-btc --windows 6 --train 0.7
```

Требование: **parity**. Один и тот же код стратегии исполняется live и в
бэктесте; отличие — только в портах (`Clock`, `Venue`, `MarketData`).

### 2.9 Журнал

```bash
vend journal tail [--follow] [--kind decision|order|risk|fill]
vend journal query --from 2026-11-01 --run <id> --jsonl
vend journal export --format jsonl|csv --out ./audit/
vend journal replay <run-id> --from <ts> --to <ts>   # детерминированный реплей
vend journal verify                                  # проверка hash-chain
```

### 2.10 Сервер и live

```bash
vend serve [--addr 127.0.0.1:8420] [--open]
vend live status
vend live unlock --account binance-main --ttl 8h    # подтверждение в TTY/UI
vend live lock
```

Live-unlock: интерактивное подтверждение (ввод последних 4 символов account id +
фраза `I UNDERSTAND`), TTL, привязка к отпечатку ключей; сбрасывается при kill
switch, рестарте демона и изменении ключей.

## 3. Форматы вывода и коды возврата

- По умолчанию — человекочитаемая таблица с цветом (отключается `NO_COLOR`/`--no-color`).
- `--json` — один объект; `--jsonl` — поток; `--csv` — плоские таблицы.
- `--quiet` — только идентификаторы/значения; `-v/-vv` — диагностика в stderr.
- Прогресс — только в TTY (stderr), в пайпах молчит.

| Код | Значение |
|---|---|
| 0 | Успех |
| 1 | Общая ошибка |
| 2 | Неверное использование/валидация флагов |
| 3 | Не найдено (run, order, provider) |
| 4 | Конфликт состояния (уже запущено, несовместимый режим) |
| 5 | Нет прав / заблокировано (live не разблокирован, ключи не заданы) |
| 6 | Ошибка провайдера (сеть, 429, 5xx, отказ биржи) |
| 7 | Отказ риск-гейта |
| 8 | Таймаут |
| 9 | Требуется подтверждение (approval) |

Ошибки — структурированные: `{code, message, hint, docsUrl, retryable, details}`.
`hint` обязателен: CLI должен говорить, что делать дальше.

## 4. Конфиг и профили

```bash
vend config path
vend config get risk.limits.maxOrderNotional
vend config set risk.limits.maxOrderNotional=500 --scope account:binance-main
vend config validate
vend config diff --scope account:binance-main
```

Иерархия scope: `global → account → run`; более специфичный переопределяет.
Изменения, влияющие на деньги (лимиты, режим, ключи), пишутся в `audit_log` с
автором и diff.

## 5. Стратегии: встроенные и внешние

### 5.1 Strategy API и `StrategySpec`

```go
type Strategy interface {
    Spec() StrategySpec
    OnStart(ctx context.Context, s State) error
    OnBar(ctx context.Context, b Bar) []Signal
    OnQuote(ctx context.Context, q Quote) []Signal
    OnTick(ctx context.Context, t Tick) []Signal
    OnOrder(ctx context.Context, o OrderEvent) []Signal
    OnTrade(ctx context.Context, f Fill) []Signal
    OnTimer(ctx context.Context, at time.Time) []Signal
    OnStop(ctx context.Context, s State)
}

type StrategySpec struct {
    Params      []ParamSpec  // name, type, default, min, max, step, side
    Timeframes  []string
    WarmupBars  int
    SupportsShort bool
}
```

Правила: нет I/O, нет системного времени, нет глобального состояния; один и тот
же код в backtest/sandbox/live; параметры валидируются по `StrategySpec`, и из
него же генерируются CLI-флаги, формы UI и пространство оптимизации.

Встроенные (Go, компилируются в бинарник): `dca`, `grid`, `trend`, `rebalance`,
`breakout`. Полный список — в `core/internal/strategy/builtin`.

Внешние — WASM-пак:

```
my-strategy/
├── pack.json          # {apiVersion, kind: Strategy, id, version, abi: v1, params}
├── strategy.wasm      # сборка из Rust/Go/TinyGo
└── README.md
```

ABI `v1` (минимальный и стабильный):

```text
exports:
  abi_version() -> u32
  init(config_ptr, len) -> i32
  on_event(event_ptr, len) -> signals_ptr   # JSON in / JSON out
  dealloc(ptr, len)
imports (host):
  log(level, ptr, len)
  indicator(name_ptr, len, params_ptr, len) -> value
  emit_diagnostic(ptr, len)
```

Правила: нет сети, нет файловой системы, нет системного времени; часы приходят в
событии; лимит памяти 64 МБ и 10M топлива на вызов; детерминизм обязателен.
Пак валидируется (`vend strategy validate`), подписывается хэшем, версия ABI
проверяется при загрузке.

## 6. Внутренний event loop (упрощённо)

```go
for {
    select {
    case <-ctx.Done():
        return shutdown()
    case ev := <-marketEvents:            // lossy, коалесцируется
        hub.Apply(ev)
        runtime.OnMarket(ev)              // стратегии, watchers
    case ev := <-criticalEvents:          // at-least-once, outbox
        journal.Append(ev)
        runtime.OnCritical(ev)            // fills, orders, risk
    case <-ticker.C:
        scheduler.Tick(now)
        health.Probe(ctx)
        reconciliation.MaybeRun(ctx)
    }
}
```

Инварианты:
- Один горутин-владелец состояния `run`; внешние вызовы — через каналы/акторы.
- Никаких блокирующих вызовов в event loop: провайдеры — с таймаутами и в пулах.
- Критические события сначала в outbox, затем в обработку (crash-safe).
- Дедуп по `eventId`; порядок на инструмент — монотонный по `seq`.

## 7. Расширения (v1 и дальше)

| Точка | v1 | v2 |
|---|---|---|
| Стратегии | встроенные Go + WASM | marketplace паков |
| Агенты | YAML-манифесты + встроенные роли | внешние агенты по gRPC |
| Провайдеры | Go-модули, компилируются | out-of-process паки (gRPC, любой язык) |
| Политики риска | YAML + встроенные проверки | CEL/Rego-выражения |
| Нотификации | telegram/webhook/push | матрица маршрутов |

Версионирование ABI: `pack.json.apiVersion` + `abi: v1`; несовместимый пак
отклоняется с понятной ошибкой, а не падает.

## 8. Пример сессии

```bash
$ vend init --template crypto-paper
✓ конфиг создан: C:\Users\me\AppData\Roaming\lv24-vending\config.yaml
✓ БД инициализирована (sqlite, WAL)
✓ paper-провайдер подключён: paper-main
! live не разблокирован — торговля только в paper

$ vend provider connect exchange-binance --id binance-main
? API key: ****
? API secret: ****
✓ ключи сохранены в Windows Credential Manager
✓ test: spot read ok, trade ok, server time skew 42ms
! права на вывод средств обнаружены — отключите их на бирже (fail closed)

$ vend run start grid-btc --account binance-main --symbol BTC/USDT --mode paper
✓ run 01J... started (paper), strategy grid v1
$ vend run inspect 01J...
  equity 10 000.00 USDT  pnl +0.42%  orders 12  fills 9  exposure 18%
  last decision 2m ago: buy 0.001 BTC @ 59 812.5 (confidence 0.71)
$ vend agent explain 01J-DEC-7
  role       analyst (llm-openaicompat/gpt-5.5)
  input      market.candles 1h x200, orderbook top20, funding
  proposal   buy 0.001 BTC, confidence 0.71, ttl 5m
  risk       PASS (7/7): notional 59.81 < 1000, exposure 18% < 60%, ...
  approval   not required (paper)
  outcome    filled @ 59 815.0, slippage 2.5 bps
```
