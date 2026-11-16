# 04. Провайдеры и контракты

Статус: draft v0.1 · Зависит от: `01-architecture.md`

## 1. Принцип

Провайдер — самостоятельный Go-модуль, который реализует один или несколько
портов ядра и регистрируется дескриптором. Ядро не знает имён вендоров: только
capability-модель, контракт и conformance-тесты.

```
providers/exchange-binance/
├── go.mod
├── provider.go        # Descriptor() + Factory()
├── execution.go       # ports.Venue
├── marketdata.go      # ports.MarketData
├── rest.go ws.go      # транспорт
├── errors.go          # маппинг ошибок вендора в доменные
├── symbols.go         # канонические символы <-> вендорские
├── ratelimit.go
├── config.schema.json # UI-форма подключения
└── conformance_test.go
```

## 2. Порты

```go
// ports/venue.go
type Venue interface {
    Descriptor() Descriptor
    Accounts(ctx context.Context) ([]domain.Account, error)
    Balances(ctx context.Context, accountID string) ([]domain.Balance, error)
    Positions(ctx context.Context, accountID string) ([]domain.Position, error)

    PlaceOrder(ctx context.Context, req domain.OrderRequest) (domain.Order, error)
    CancelOrder(ctx context.Context, req domain.CancelRequest) (domain.Order, error)
    CancelAll(ctx context.Context, accountID string, filter domain.OrderFilter) ([]domain.Order, error)
    Order(ctx context.Context, ref domain.OrderRef) (domain.Order, error)
    OpenOrders(ctx context.Context, accountID string, filter domain.OrderFilter) ([]domain.Order, error)
    Fills(ctx context.Context, accountID string, since time.Time) ([]domain.Fill, error)
}

// ports/marketdata.go
type MarketData interface {
    Descriptor() Descriptor
    Instruments(ctx context.Context) ([]domain.Instrument, error)
    Candles(ctx context.Context, req domain.CandleRequest) ([]domain.Candle, error)
    OrderBook(ctx context.Context, ref domain.InstrumentRef, depth int) (domain.OrderBook, error)
    Trades(ctx context.Context, ref domain.InstrumentRef, limit int) ([]domain.Trade, error)
    Subscribe(ctx context.Context, subs []domain.Subscription) (<-chan domain.MarketEvent, error)
    Health(ctx context.Context) domain.ProviderHealth
}

// ports/llm.go
type LLM interface {
    Descriptor() Descriptor
    Models(ctx context.Context) ([]domain.ModelInfo, error)
    Chat(ctx context.Context, req domain.ChatRequest) (domain.ChatResponse, error)
    Stream(ctx context.Context, req domain.ChatRequest, onChunk func(domain.ChatChunk) error) error
}

// ports/decision.go
type Decision interface {
    Descriptor() Descriptor
    Info(ctx context.Context) (domain.DecisionInfo, error)
    Decide(ctx context.Context, req domain.DecisionRequest) (domain.DecisionResponse, error)
}

// ports/notifier.go
type Notifier interface {
    Descriptor() Descriptor
    Send(ctx context.Context, msg domain.Notification) error
}
```

Все методы обязаны уважать `ctx` (таймаут/отмена), не паниковать, возвращать
типизированные ошибки.

### 2.1 Композиция, а не god-интерфейс

`Venue` и `MarketData` — **композиция узких capability-интерфейсов**. Провайдер
реализует то, что умеет; неподдержанное — ошибка композиции при регистрации, а
не исключение в рантайме.

```go
type ExecutionProvider interface { PlaceOrder; CancelOrder; CancelAll; Order; OpenOrders }
type AccountProvider   interface { Accounts; Balances; Positions }
type Venue             interface { ExecutionProvider; AccountProvider; Fills }

type InstrumentProvider interface { Instruments }
type CandleProvider     interface { Candles }
type BookProvider       interface { OrderBook }
type TradeProvider      interface { Trades }
type StreamProvider     interface { Subscribe }
type MarketData          interface { InstrumentProvider; CandleProvider; BookProvider; TradeProvider; StreamProvider; Health }
```

Общие сервисы ядра (не провайдера): `InstrumentRegistry` (канонические символы,
алиасы, tick/lot, TTL+disk cache), `CandleService` (пагинация, дедуп, gap-детект
и бэкфилл), `BookStream` (snapshot+delta, seq/nonce, checksum top-N),
`ConnectionSupervisor` (stale, backoff+jitter, resubscribe, health).

### 2.2 Один нормализованный поток для live и истории

Нормализация — чистые функции-мапперы (`Mapper{CanHandle, Map, GetFilters}`),
которые используются и для реплея, и для стрима. Семантика фиксируется письменно
и тестируется snapshot-тестами:

- `side` у сделки — сторона **тейкера**;
- `isSnapshot` у стакана — сброс книги, дельта без снапшота не эмитится;
- `amount = 0` — удаление уровня;
- конверт события: `{type, provider, instrumentId, tsEvent, tsRecv, seq, snapshot, gap, payload}`,
  деньги/количества — decimal-строки;
- stale определяется отсутствием данных, а не heartbeat; после реконнекта
  сбрасывается всё состояние подписки.

Это устраняет главный источник расхождений «бэктест ≠ live»: и там и там один
код маппинга и одна семантика.


## 3. Доменные модели (фрагмент)

```go
type Instrument struct {
    ID            string          // "binance:spot:BTCUSDT"
    Venue         string
    Symbol        string          // канонический "BTC/USDT"
    VenueSymbol   string          // "BTCUSDT"
    Class         AssetClass      // spot|margin|futures|option|fx|equity
    Base, Quote   string
    TickSize      Decimal
    StepSize      Decimal
    MinNotional   Decimal
    ContractSize  Decimal         // 1 для спота
    Limits        InstrumentLimits
    Meta          map[string]string
}

type OrderRequest struct {
    AccountID     string
    InstrumentRef InstrumentRef
    Side          Side            // buy|sell
    Type          OrderType
    Qty           Decimal
    Price         *Decimal        // nil для market
    StopPrice     *Decimal
    TIF           TIF
    ReduceOnly    bool
    PostOnly      bool
    Leverage      *Decimal
    ClientOrderID string          // идемпотентность, генерируется OMS
    Meta          map[string]string
}

type Order struct {
    ID, ClientOrderID string
    AccountID         string
    InstrumentRef     InstrumentRef
    Status            OrderStatus   // new|accepted|partially_filled|filled|canceled|rejected|expired
    Side, Type, TIF   ...
    Qty, FilledQty    Decimal
    Price, AvgPrice   Decimal
    Fees              []Fee
    CreatedAt, UpdatedAt time.Time
    RejectReason      string
    Raw               json.RawMessage // аудит, redacted
}
```

Деньги — `Decimal` (фиксированная точность, парсинг из строки). Никаких `float64`
в домене.

## 4. Capability-модель: три уровня и четыре исхода

Возможность — **не свойство вендора**. Она определяется пересечением трёх уровней:

```
Effective = ProviderDescriptor ∩ AccountContext ∩ InstrumentDescriptor
```

| Уровень | Что описывает | Примеры |
|---|---|---|
| `ProviderDescriptor` | что реализует адаптер | order types, TIF, streams, batch, sandbox |
| `AccountContext` | что разрешено счёту | entitlements, margin/cash, PDT, лицензия (TBNZ/TBSG), unlock, права котировок |
| `InstrumentDescriptor` | что имеет смысл для бумаги | `stop_supported`, `net_position`, min/max, fractional, shortable |

Исход — один из четырёх, всегда с reason-code:

```go
type CapabilityOutcome string // NATIVE | EMULATED | DEGRADED | UNSUPPORTED
```

| Исход | Значение | Пример |
|---|---|---|
| `NATIVE` | вендор делает сам | stop-order на Binance futures |
| `EMULATED` | ядро делает за вендора, семантика сохранена | trailing stop локально, fractional через stepSize |
| `DEGRADED` | работает, но с потерей свойства | GTD → DAY + перевыставление |
| `UNSUPPORTED` | нельзя, отказ **до** отправки | OCO на споте без поддержки |

Правила:

- **Deny до отправки**, не после. Отказ приходит с кодом причины и списком
  альтернатив (другой инструмент/тип ордера/провайдер).
- `reduce_only`/`position_effect` **никогда не понижаются молча**: если вендор не
  умеет — отказ, а не «сделаем обычный ордер».
- Деградация объявляется провайдером и покрывается тестом; в `vend provider show`
  и в UI видна таблица `(order_type, tif) → outcome`.
- Effective capability кэшируется с TTL и инвалидируется при смене подключения,
  счёта или инструмента. `vend provider capabilities --account X --instrument Y`.

### 4.1 Правила вендора — объект, а не `if`

Правила («можно ли подать/изменить/отменить», плечо, buying power, комиссии,
shortable) живут в отдельном объекте-политике, который возвращает адаптер:

```go
type VenuePolicy interface {
    CanSubmit(ctx context.Context, req domain.OrderRequest, acc domain.AccountSnapshot) domain.PolicyDecision
    CanAmend(ctx context.Context, ord domain.Order, req domain.AmendRequest) domain.PolicyDecision
    CanCancel(ctx context.Context, ord domain.Order) domain.PolicyDecision
    BuyingPower(ctx context.Context, acc domain.AccountSnapshot) domain.Money
    Fees(ctx context.Context, req domain.OrderRequest) []domain.Fee
}
```

`RiskGate` вызывает `CanSubmit` как часть проверок 7–9; вердикт и reason-code
попадают в журнал. Это позволяет добавлять брокера, не трогая риск-модуль.

### 4.2 Сессии и entitlements

У брокеров сессии живут меньше, чем процесс: IB farm 2104, Tradier stream-session
~4.9 мин, Tiger token 15 дней, Futu unlock, OANDA stream. Единый
`SessionSupervisor`: TTL, re-auth, unlock, resubscribe, health; при истечении —
`degraded`, входы блокируются, выходы работают.


## 5. Instrument master, символы и точность

Симвология — отдельный сервис, а не строковая функция в адаптере:

- Канонический вид: `BTC/USDT`, `AAPL`, `EUR/USD`, `BTC/USDT:PERP`.
- Вендорские ключи хранятся рядом: IB `conId`, Futu `US.AAPL`, Tiger
  `symbol+sec_type`, OANDA `EUR_USD`, OCC-код опциона, MT5-суффиксы.
- Алиасы и правила разрешения — в `InstrumentRegistry` (TTL + disk cache),
  с проверкой tick/lot/contract multiplier.
- Округление: всегда вниз по `StepSize` (количество) и к ближайшему `TickSize`
  (цена) с учётом стороны (buy — вверх, sell — вниз для лимита), затем проверка
  `MinNotional`; иначе `ErrInvalidOrder` **до** отправки.
- Неизвестный инструмент → `ErrNotFound`, не «попробуем как есть».
- Amend semantics: `in_place | cancel_replace | replace_new_id` объявляется
  провайдером; OMS не предполагает, что id ордера сохранится.

## 6. Ошибки и ретраи

```go
type ErrorCode string
const (
    ErrAuth            ErrorCode = "auth"
    ErrPermission      ErrorCode = "permission"
    ErrRateLimit       ErrorCode = "rate_limit"
    ErrInsufficient    ErrorCode = "insufficient_funds"
    ErrInvalidOrder    ErrorCode = "invalid_order"
    ErrRejected        ErrorCode = "rejected"
    ErrDuplicate       ErrorCode = "duplicate"
    ErrNotFound        ErrorCode = "not_found"
    ErrUnavailable     ErrorCode = "unavailable"
    ErrTimeout         ErrorCode = "timeout"
    ErrUnknown         ErrorCode = "unknown"
)
```

| Код | Retry | Действие OMS |
|---|---|---|
| `rate_limit` | да, по `Retry-After` | пауза, backoff, без дублей |
| `unavailable`/`timeout` | да, ограниченно | повтор с тем же `clientOrderId` |
| `auth`/`permission` | нет | провайдер `down`, run pause, alert |
| `insufficient_funds` | нет | отказ риска, alert |
| `invalid_order` | нет | баг стратегии, лог + alert |
| `rejected` | нет | фиксация причины от вендора |
| `unknown` | осторожно | сначала `Order(ref)` для выяснения статуса |

Вендорские коды сохраняются в `details.vendorCode` и `raw` (redacted), чтобы
поддержка могла разобраться без воспроизведения.

## 7. Rate limits и здоровье

- Каждый провайдер получает `RateLimiter` из `Deps`, лимиты — из дескриптора.
- Адаптивность: 429/`Retry-After` понижает эффективную скорость, восстановление
  постепенное (token bucket с динамическим burst).
- `Health` — `ok|degraded|down` + `latencyMs`, `lastError`, `stalenessMs`,
  `serverTimeSkewMs`. Проверка: при старте, каждые 30 с (лёгкий ping), после
  серии ошибок.
- `degraded` не блокирует выходы (стопы), но блокирует входы согласно политике.

## 8. Идемпотентность и reconciliation

- `clientOrderId` генерируется OMS: `vm-<runShort>-<seq>-<rand4>`, хранится в БД
  до отправки.
- Повтор после таймаута — с тем же id; вендорский дубликат → `ErrDuplicate`,
  затем `Order(ref)` для актуализации.
- Reconciliation: при старте демона, после реконнекта WS, каждые 60 с и перед
  каждым новым входом (лёгкая сверка позиции).
- Расхождение: событие `reconcile.mismatch` → пауза run (если критично) →
  авто-исправление по политике (`adopt_venue` | `pause_and_alert`).
- Балансы и позиции — всегда из вендора; локальная БД — кэш с `asOf`.

## 9. LLM-провайдеры

Базовый контракт — OpenAI-совместимый (`/v1/chat/completions`), потому что его
поддерживают почти все: OpenAI, Azure, OpenRouter, Together, Groq, Fireworks,
vLLM, LM Studio, Ollama (через совместимый слой), DeepSeek, Qwen и т.д.

```go
type ChatRequest struct {
    Model       string
    Messages    []Message      // role, content[], tool_calls
    Tools       []ToolSpec
    Temperature *float64
    MaxTokens   *int
    JSONSchema  json.RawMessage // structured output, если поддерживается
    Stream      bool
    Meta        RequestMeta     // агент, run, decision, budget
}
```

Нормализация: tool-calls, JSON-mode, vision (изображения как вложения),
usage/cost, ошибки (`context_length`, `rate_limit`, `content_filter`).
Возможности модели (`tools`, `vision`, `json`, `context`, `price`) — в
`Capabilities.LLM` и в `Models()`; роутер не выберет модель без нужной фичи.

Локальные модели: `llm-openaicompat` умеет `baseURL: http://127.0.0.1:11434/v1`
(Ollama), `llama.cpp server`, `vLLM`, `LM Studio` — отдельный провайдер не нужен.

## 10. Decision-провайдеры (Drex/Jev)

Decision-модель возвращает вероятности по заданным вариантам, без генерации
текста. Это самый дешёвый и самый предсказуемый слой для «да/нет/сколько».

```go
type DecisionRequest struct {
    Model     string        // pinned id, алиасы в prod запрещены
    State     State         // текстовые части контекста
    Assets    map[string]Image // ref -> изображение (график, скриншот)
    Questions []Question
    Meta      RequestMeta   // decisionId, timeoutMs, budget, idempotencyKey
}

type Question struct {
    ID           string
    Type         QuestionType // choice | noul | score
    Instructions string
    Criteria     string
    Options      []Option     // порядок = порядок вариантов в ответе
}

type DecisionResponse struct {
    Answers   map[string]Answer // {choice, probabilities, confidence, legend}
    Model     string
    Usage     Usage             // inputTokens, outputTokens, cost, latencyMs
    RequestID string
    Raw       json.RawMessage
}
```

Wire-формат System One (эталон — Drex `POST /v1/systemone`, Jev совместим).
Исправлено в v0.2 по документации вендора (ADR-0010; источники —
[API reference](https://console.nace.ai/docs/drex/api-reference/systemone) и
`docs/request-format.md` репозитория `nace-ai/drex-decision-models`):

```json
{
  "model": "drex-max-v1.1",
  "state": {"symbol": "BTC/USDT", "tf": "1h", "close": "59812.5", "ema200": "58210.5", "volume_z": 2.3},
  "questions": {
    "action": {
      "type": "choice",
      "instructions": "Какое действие на следующем баре?",
      "criteria": {
        "buy": "тренд вверх подтверждён объёмом",
        "sell": "тренд вниз подтверждён объёмом",
        "hold": "нет подтверждённого направления"
      }
    },
    "size_ok": {"type": "noul", "instructions": "Нотионал в пределах политики?"},
    "setup": {"type": "score", "instructions": "Качество сетапа", "criteria": ["слабый", "рабочий", "сильный"]}
  }
}
```

Ответ:

```json
{
  "model": "drex-max-v1.1",
  "answers": {
    "action": {"type": "choice", "choice": "buy", "confidence": 0.43,
               "probabilities": {"buy": 0.62, "sell": 0.11, "hold": 0.27}},
    "size_ok": {"type": "noul", "noul": 0.91},
    "setup": {"type": "score", "score": 1.3362, "legend": ["слабый", "рабочий", "сильный"],
              "probabilities": {"0": 0.2489, "1": 0.166, "2": 0.5851}, "confidence": 0.6681}
  },
  "usage": {"input_tokens": 1420, "output_tokens": 12},
  "request_id": "…",
  "evaluation_time_ms": 104
}
```

Ключевые детали контракта:

- Варианты всех типов передаются в `criteria`: объект «ключ → описание» для
  `choice`, упорядоченный массив уровней для `score`, необязательные
  `criteria.true`/`criteria.false` для `noul`. Поля `options` на проводе нет.
- Порядок ключей `criteria` задаёт порядок вариантов. Go-`map` порядок теряет,
  поэтому в порте варианты — срез, а JSON запроса адаптер собирает сам.
- `noul` в ответе — число от 0 до 1 (вероятность «да»), без `probabilities`.
  Облачный Drex добавляет `confidence = max(noul, 1 − noul)`; локальный сервер — нет.
- `confidence` для `choice` — **концентрация**, а не P(правоты):
  `C = (p_max − 1/n) / (1 − 1/n)`. При двух вариантах `C = 0.5` ⇔ `p_max = 0.75`.
  Порог в политике ставится на `p_max` исполняемого варианта, отдельно на вопрос.
  Адаптер пересчитывает `C` и сверяет с ответом. Для `score` формула вендора
  различается между облаком и локальным сервером — её не пересчитываем.
- Изображения: поле `images` (до 8) в JSON либо multipart с `assets`; только для
  Drex Max. Заголовок `TypeSafe-Danger-Zone-Images` у Drex принимается, но не
  обязателен; для Jev ресёрч указывает его обязательным — перепроверить по
  документации TypeSafe при реализации изображений.
- Батч до 1024 запросов (`requests`) — свойство локального `serve.py`. У облачного
  API отдельный асинхронный Batches со скидкой.
- `output_tokens` — токены сериализованного ответа, не тарифицируются; тарифицируется
  только `input_tokens` (изображения входят в него). Цена хранится decimal-строкой
  за 1B токенов (ADR-0009).
- Ответ содержит **фактическую** модель. Запрошенный id не гарантирует
  неизменность: вендор заменял модель под тем же id, а снятые id обслуживает
  преемник. В журнал пишется модель из ответа; смена — событие
  `decision.model_changed` (ADR-0010). Алиасы (`drex-latest`, `jev-latest`) в prod
  запрещены.
- Ошибки облачного API: 401 `authentication_error`, 402 `insufficient_credit`,
  422 `invalid_request_error`, 429 `rate_limit_error`, 500, 503, 529 `overloaded`.

Адаптер `decision-drex` работает с `POST /v1/systemone` (state + questions +
images, ответ — вероятности); профиль `jev` — wire-compatible (меняются base URL
и заголовки). Один код, два профиля конфигурации.

Обязательные свойства адаптера:
- таймаут и `circuit breaker`; при недоступности — `ErrUnavailable`;
- ответ `choice` валидируется: набор ключей `probabilities` равен набору
  вариантов, значения конечны и лежат в [0, 1], `|Σp − 1| ≤ 0.001`,
  `choice` — argmax, пересчитанная `confidence` отличается не более чем на 0.02;
  нарушение — `invalid_output`, а не «попробуем разобрать»;
- всё, что ушло и вернулось, сохраняется в `decision_inputs`/`raw` (хэш + redacted).

Политика поверх провайдера (не внутри): порог уверенности, fallback на другую
модель, «abstain» вместо действия, обязательный второй голос (critic).

## 11. Каталог

`providers/catalog.yaml` (генерируется из дескрипторов + ручные записи) —
справочник «что существует», доступный UI до установки пака:

```yaml
- id: exchange-okx
  kind: venue
  displayName: OKX
  auth: [api_key_secret_passphrase]
  assetClasses: [spot, margin, futures, options]
  docs: https://www.okx.com/docs-v5/
  status: planned          # installed | available | planned
```

`vend provider catalog` показывает и установленные, и доступные, и запланированные
— это карта развития, а не обещание.

## 12. Conformance-тесты

`core/porttest` — обязательный набор для каждого провайдера:

| Набор | Проверяет |
|---|---|
| `VenueConformance` | округление qty/price, minNotional, duplicate id, cancel flow, partial fill, error mapping, cancel-all, пагинация ордеров |
| `MarketDataConformance` | порядок свечей, дедуп, gap-детект, resubscribe, лимиты подписок, таймфреймы |
| `LLMConformance` | стриминг, tool-calls, JSON-mode, таймауты, retry-after, usage |
| `DecisionConformance` | несколько вопросов, изображения, невалидный ответ, таймаут, стоимость |

Провайдер без проходящего conformance-набора не попадает в релиз (CI job
`providers`). Тесты используют `httptest` и записанные фикстуры, без реальной сети.

## 13. Матрица v1

| Провайдер | Kind | v1 | Заметки |
|---|---|---|---|
| `paper` | venue+marketdata | ✅ | встроенный симулятор, данные с провайдера данных |
| `exchange-binance` | venue+marketdata | ✅ | spot + USDⓈ-M futures, user data stream |
| `exchange-bybit` | venue+marketdata | ✅ | spot + linear perp |
| `exchange-okx` | venue+marketdata | v1.1 | passphrase, options |
| `marketdata-ccxt` | marketdata | ✅ | REST-мост для длинного хвоста (без ордеров) |
| `broker-alpaca` | venue+marketdata | ✅ | equities, paper по умолчанию |
| `broker-ibkr` | venue+marketdata | v1.2 | TWS/Gateway, ограничения синхронизации |
| `llm-openaicompat` | llm | ✅ | OpenAI, OpenRouter, Groq, Ollama, vLLM |
| `llm-anthropic` | llm | ✅ | нативный API |
| `llm-google` | llm | v1.1 | Gemini |
| `decision-drex` | decision | ✅ | `drex-max`, `drex-max-finance` |
| `decision-local` | decision | v1.2 | ONNX/llama.cpp, офлайн |
| `notify-telegram` | notifier | ✅ | команды approve/kill из чата (v1.1) |
| `notify-webhook` | notifier | ✅ | HMAC-подпись |

## 14. Как это закрывает «фичи вендора → свой агент»

1. Провайдер объявляет возможности (например, у Binance futures есть
   `reduce_only`, `leverage`, `post_only`).
2. Агент-пак объявляет требования и область применения:
   ```yaml
   spec:
     appliesTo: {venues: [exchange-binance], assetClasses: [futures]}
     requires:  {features: [reduce_only, leverage], data: [funding, open_interest]}
   ```
3. Резолвер при старте: если требования не выполнены — агент не загружается, в
   UI видно почему («нужен funding/OI, провайдер не даёт»).
4. Если у вендора фича отсутствует, но ядро умеет эмуляцию — она включается
   только при явной политике и помечается в журнале.
5. Матрица `vend agent matrix` показывает, какие роли покрыты для каждого
   вендора и где пробелы.
