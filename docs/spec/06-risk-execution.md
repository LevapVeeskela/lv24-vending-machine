# 06. Риск, исполнение, аудит

Статус: draft v0.1 · Зависит от: `01-architecture.md`, `04-providers.md`

## 1. Инвариант

```
любое действие с деньгами = OrderIntent → RiskGate → Reservation → OMS → Venue
```

Нет пути к бирже в обход `RiskGate`. Нет side-effect до `ALLOW`. Нет ретрая без
сверки. Нет «тихой починки» расхождений.

## 2. OrderIntent

```go
type OrderIntent struct {
    ID            string        // ULID
    ClientOrderID string        // идемпотентный ключ, генерируется здесь
    CreatedAt     time.Time
    ExpiresAt     time.Time     // цена/размер протухают
    RunID         string
    AccountID     string
    Source        Source        // strategy | agent | manual | rebalance
    AgentID       string        // если источник — агент
    Instrument    InstrumentRef
    Side          Side
    Type          OrderType
    Qty           Decimal
    Price         *Decimal
    TIF           TIF
    ReduceOnly    bool
    PostOnly      bool
    IntentKind    IntentKind     // entry | exit | stop | rebalance | cancel
    MandateHash   string         // хэш политики, под которой действует run
    Evidence      []EvidenceRef  // ссылки на decision/decision_inputs
    Reason        string
}
```

`INTENT_KIND = exit|stop` проходит **сокращённый** набор проверок и не зависит от
доступности ИИ: закрыть позицию можно всегда (кроме kill switch и явного запрета
площадки).

## 3. RiskGate — чистая функция

```go
type Verdict string // ALLOW | REJECT | DEFER

type Decision struct {
    Verdict    Verdict
    Code       RejectCode      // стабильный машиночитаемый код
    Scope      Scope           // order | account | portfolio | run | system
    Message    string
    Checks     []CheckResult   // все выполненные, по порядку
    PolicyID   string
    PolicyVer  string
    PolicyHash string
    SnapshotID string          // хэш снапшота, на котором считали
    DecidedAt  time.Time
    RetryAfter *time.Duration   // для DEFER
}

func Evaluate(intent OrderIntent, snap Snapshot, policy PolicyBundle) Decision
```

Свойства:
- **Детерминизм**: одинаковые `intent + snap + policy` → одинаковый вердикт.
- **Fail-fast фиксированный порядок**: порядок проверок — часть контракта и
  версионируется вместе с политикой (иначе вердикт невоспроизводим).
- **Полный след**: возвращаются все проверки до первой блокирующей.
- **Чистота**: без сети, без БД, без времени изнутри (время в снапшоте).

### 3.1 Порядок проверок (v1)

| # | Проверка | Код отказа | Scope |
|---|---|---|---|
| 1 | Схема/валидность intent (decimal, side, tif) | `invalid_intent` | order |
| 2 | Свежесть данных (`data_age_ms` ≤ порог) | `stale_data` | system |
| 3 | Режим и права источника (`paper/live`, agent permissions) | `mode_denied` / `not_authorized` | account |
| 4 | Kill switch и аварийные флаги | `kill_switch` / `trading_halted` | system |
| 5 | Идемпотентность (`clientOrderId` не использован) | `duplicate_intent` | order |
| 6 | TTL цены/интента | `expired_intent` | order |
| 7 | Торгуемость: `VenuePolicy.CanSubmit`, статус инструмента, сессия, allowlist | `not_tradable` | order |
| 8 | Размер/цена: step, tick, minNotional, maxOrderNotional | `size_violation` / `price_violation` | order |
| 9 | Price collar: отклонение от mark/последней цены | `price_collar` | order |
| 10 | Rate limit ордеров + число открытых ордеров | `rate_limit` / `too_many_open_orders` | account |
| 11 | Позиция: max per instrument, reduce-only корректность | `position_limit` | account |
| 12 | Экспозиция портфеля: gross/net, концентрация, корреляционный кластер | `exposure_limit` | portfolio |
| 13 | Ликвидность: доля от объёма/глубины, оценка impact | `liquidity_limit` | order |
| 14 | Средства/маржа: доступный баланс, margin ratio, плечо | `insufficient_funds` / `margin_limit` | account |
| 15 | Drawdown/дневной убыток + статус reconciliation | `drawdown_limit` / `reconciliation_pending` | portfolio |

Все проверки — из `policy` (значения, пороги, включённость). Набор кодов
стабилен; новые коды добавляются, старые не переиспользуются.

Проверки 7–9 консультируются с `VenuePolicy` провайдера (`CanSubmit`,
buying power, fees): отказ приходит **до** отправки, с reason-code вендора.
Effective capability (`NATIVE|EMULATED|DEGRADED|UNSUPPORTED`) учитывается как
отдельный фактор: `UNSUPPORTED` → отказ, `DEGRADED` → пометка в журнале и,
если политика требует, отказ.

### 3.2 Reservation (две фазы)

```
ALLOW → reserve(order)      # занять лимиты/баланс/слот ордера
      → submit to venue
      → commit(reservation)  # по ack/филлу
      → rollback(reservation) # по reject/таймауту
```

Резервация не даёт двум параллельным интентам превысить общий лимит. Без неё
проверка «по снапшоту» гоняется. Резервации живут в БД и восстанавливаются после
рестарта.

### 3.3 DEFER, а не «подождём в коде»

`DEFER` возвращается с `retry_after` (rate limit, свежесть данных, reconciliation
in progress). OMS переставляет intent в очередь, а не блокирует горутин.

## 4. Идемпотентность и неизвестный исход

- `clientOrderId` = `vm-<run8>-<seq6>-<rand4>`; пишется в БД **до** отправки.
- Таймаут/обрыв: статус `PENDING_RECONCILE`, **без слепого ретрая**. Сначала
  `Order(ref)`, потом решение: подтвердить, отменить или выпустить новый intent.
- Дубликат `clientOrderId` у вендора: трактуем как «ордер уже есть», а не как
  ошибку (поведение openexch: 200 с существующим ордером).
- Все переходы ордера — события в журнале: `order.intent`, `order.submitted`,
  `order.ack`, `order.partial`, `order.filled`, `order.canceled`,
  `order.rejected`, `order.unknown`.
- Дедуп филлов по `(venueTradeId)` + сверка суммы исполненного с `FilledQty`.

## 5. Reconciliation

| Триггер | Что сверяем | Действие |
|---|---|---|
| Старт демона | открытые ордера, позиции, балансы, «сироты» | `StartupRebuild`: подхватить/терминализировать |
| Реконнект WS | пропущенные события с `since` | догрузка, дедуп |
| Каждые 60 с | открытые ордера, позиции | лёгкая сверка |
| Перед входом | позиция по инструменту | быстрый чек |
| По alert провайдера | всё | полная сверка |

Расхождение → `reconcile.mismatch`:
- `critical` (позиция/баланс не совпали) → **пауза run**, блокировка входов,
  alert; авто-исправление только по политике (`adopt_venue` требует approve).
- `minor` (лишний закрытый ордер в кэше) → исправить кэш, записать в журнал.

Правило: локальная БД — кэш вендора, а не источник правды по деньгам.

## 6. Kill switch

Области: `global`, `account`, `run`, `instrument`. Действие: отменить открытые
ордера (по политике), запретить новые intents, сбросить live-unlock, уведомить.

- Включение: CLI/UI/API, автоматически по критическому риску (drawdown, reconcile
  critical, серия ошибок провайдера), из Telegram (v1.1).
- Выключение: только с подтверждением; для `global` — two-person rule в серверном
  режиме (два разных admin), в локальном — явное подтверждение + запись в аудит.
- Kill switch **не отключает** выходы: если позиция открыта, экстренное закрытие
  разрешено отдельной командой `vend risk flatten` (тоже под подтверждением).

## 7. Политики

```yaml
apiVersion: vending.lv24/v1
kind: RiskPolicy
metadata: {id: default, version: 1.3.0}
spec:
  mode: paper
  limits:
    maxOrderNotional: "1000"
    maxPositionNotional: {default: "3000", "BTC/USDT": "5000"}
    maxGrossExposure: "10000"
    maxNetExposure: "6000"
    maxConcentrationPct: 40
    maxLeverage: 2
    maxOpenOrders: 20
    maxOrdersPerMinute: 10
    maxDailyLoss: "250"
    maxDrawdownPct: 12
    priceCollarPct: 2.0
    maxSlippageBps: 15
    minLiquidityAdvPct: 0.5
  cooldowns:
    perInstrument: 30s
    afterLoss: 5m
    afterReject: 60s
  approval:
    required: {live_entry: true, aboveNotional: "500", newInstrument: true}
    channel: [desktop, telegram]
    ttl: 10m
  staleDataMs: 5000
  onDataLoss: block_entries
```

- `PolicyBundle` = набор политик + версия + хэш; `policy_hash` попадает в каждый
  вердикт и в `MandateHash` агента.
- Изменение лимитов — аудируемое событие с diff и автором.
- **Shadow mode**: новая политика считается параллельно (`shadow_verdict`), но не
  применяется; сравнение вердиктов на исторических intent'ах → отчёт diff перед
  включением.
- Снижение лимитов применяется мгновенно; повышение — через approve.

## 8. Sizing и портфель

- Размер считает **policy**, не агент: `target = min(policy_max, k * equity *
  risk_per_trade / stop_distance, liquidity_cap)`.
- `clamp`-модель: агрегатор только **уменьшает** книгу (монотонно), никогда не
  перераспределяет освободившуюся экспозицию между инструментами.
- Пересчёт после каждого филла/изменения equity; округление вниз по `stepSize`.
- Корреляционные кластеры (BTC/ETH = один кластер) ограничивают суммарную
  экспозицию.

## 9. Модель издержек

Оценка до отправки: `spread/2 + commission + impact`, где
`impact ≈ k * sqrt(notional / ADV)`. Если оценка > `maxSlippageBps` или сделка не
проходит no-trade band — `REJECT(liquidity_limit)`. После филла фактический
slippage пишется в событие и участвует в метриках агента/стратегии.

## 10. Paper-брокер

- Две книги: `orders` (жизненный цикл) и `ledger` (балансы/позиции), обновляются
  атомарно в одной транзакции.
- Модель исполнения: next-tick или next-bar fill, настраиваемые
  `fill_model: touch|mid|cross`, slippage bps, комиссии maker/taker, частичные
  филлы по доступной глубине.
- Данные — от реального `marketdata`-провайдера (paper не выдумывает цены).
- Тот же OMS и тот же RiskGate, что в live: отличается только `Venue`-адаптер.
- `vend backtest` использует тот же paper-брокер + виртуальные часы → parity.

## 11. Аудит

Append-only журнал с hash-chain:

```json
{
  "seq": 10423,
  "eventId": "01J...",
  "kind": "risk.verdict",
  "ts": "2026-11-16T10:00:01.234Z",
  "runId": "01J...",
  "decisionId": "01J...",
  "payload": {...},
  "policyHash": "sha256:...",
  "codeVersion": "0.1.0+git.abc123",
  "snapshotHash": "sha256:...",
  "prevHash": "sha256:...",
  "hash": "sha256:..."
}
```

- `vend journal verify` проверяет цепочку; разрыв = тревога.
- Экспорт: JSONL/CSV с манифестом (версии, хэши, период).
- Retention: события решений/ордеров — бессрочно (локально — по настройке),
  метрики — 90 дней по умолчанию.

## 12. Метрики качества исполнения

- Slippage (bps) по инструменту/агенту/типу ордера; fill rate; время до филла.
- Reject rate по кодам; доля `PENDING_RECONCILE`; число расхождений.
- Latency: intent → submit → ack; event loop lag.
- P&L после издержек: по стратегии, агенту, модели, режиму рынка.
- Analytics-пак: Sharpe, Sortino, max drawdown, Calmar, profit factor, win rate,
  exposure, turnover (формулы — из quantstats/pyfolio-подхода, реализация своя).

## 13. Чек-лист перед live

- [ ] Paper ≥ 2 недели без критических расхождений.
- [ ] Reconciliation проходит на рестарте и реконнекте (тест).
- [ ] Kill switch проверен в бумажном режиме, включая flatten.
- [ ] Лимиты заданы и покрывают худший ожидаемый размер.
- [ ] Ключи без права вывода средств; IP-allowlist на бирже.
- [ ] Агенты имеют `invalidation` и TTL; critic/veto включён для входов.
- [ ] Approve-очередь работает в desktop и Telegram.
- [ ] Бэкап БД и экспорт журнала настроены.
- [ ] Мониторинг: алерты на reconcile, drawdown, ошибки провайдера, стоимость ИИ.
