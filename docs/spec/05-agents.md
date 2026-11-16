# 05. Агенты, модели, калибровка

Статус: draft v0.1 · Зависит от: `01-architecture.md`, `04-providers.md`

## 1. Три разных сущности

| Сущность | Природа | Право на side-effect | Детерминизм |
|---|---|---|---|
| `Strategy` | код/WASM | нет, только `Signal` | да |
| `Agent` | ИИ-роль (LLM или decision-модель) | нет, только `Proposal` | нет |
| `Policy` | код/конфиг | да (вето, sizing, approve) | да |

Смешивать нельзя: если решение принимает модель, оно всё равно проходит
`RiskGate` и исполняется детерминированным OMS. Если решение принимает стратегия,
она не обязана звать ИИ вообще — и это самый быстрый режим.

## 2. Роли агентов

| Роль | Задача | Вход | Выход |
|---|---|---|---|
| `analyst` | Оценить рынок, предложить действие | свечи, стакан, индикаторы, новости | `Proposal` |
| `critic` | Найти дыры в proposal, дать контр-аргумент | proposal + те же данные | `Verdict` (support/oppose/abstain) |
| `risk` | Проверить с учётом контекста (не вместо RiskGate) | proposal + портфель | `RiskOpinion` |
| `executor` | Выбрать способ исполнения (лимит/маркет, цена, TIF) | proposal + стакан | `ExecutionPlan` |
| `venue-specialist` | Учесть особенности вендора (funding, лимиты, правила) | capabilities + состояние | `Constraints`/`Advice` |
| `researcher` | Собрать контекст (новости, on-chain, отчётность) | запросы | `ResearchNote` |
| `supervisor` | Разрешить конфликт ролей, выбрать итог | proposals + verdicts | `FinalDecision` |

Правило: **ровно один владелец финального решения** на run — либо стратегия, либо
`supervisor`. Остальные — совещательные. Так не бывает «двух капитанов».

## 3. Манифест агента (пак)

```yaml
apiVersion: vending.lv24/v1
kind: Agent
metadata:
  id: crypto-momentum-analyst
  version: 1.2.0
  displayName: Crypto Momentum Analyst
  license: MIT
spec:
  role: analyst
  appliesTo:
    venues: [exchange-binance, exchange-bybit]
    assetClasses: [spot, futures]
    instruments: ["BTC/*", "ETH/*"]
  requires:
    capabilities: [candles, orderbook, funding_rate]
    data: [market.candles, market.orderbook]
  model:
    provider: llm-openaicompat     # ссылка на connection или провайдер
    model: gpt-5.5
    fallback: [local/llama-4-70b]
    params: {temperature: 0.2, maxTokens: 1200}
    budget: {perRunUsd: 0.05, dailyUsd: 2.00}
  tools:
    - market.indicators
    - market.orderbook_summary
    - news.search: {maxResults: 5}
  output:
    schema: proposal/v1
    requireEvidence: true
    requireRationale: true
    ttl: 5m
  permissions:
    - read.market
    - read.account
    - propose.order
  schedule:
    mode: event                 # event | cron
    on: [market.candle.close]
    every: 5m
    jitter: 10s
    marketHoursOnly: true
  limits:
    timeout: 25s
    maxCallsPerHour: 60
    maxConcurrent: 2
  guardrails:
    minConfidence: 0.55
    forbiddenActions: [withdraw, transfer]
    maxProposalsPerHour: 12
```

Манифест валидируется схемой `contracts/jsonschema/agent/v1.json`. Агент без
`output.schema`, без `permissions` или с неизвестным tool не загружается.

## 4. Выход агента

```json
{
  "schemaVersion": "proposal/v1",
  "proposalId": "01J...",
  "runId": "01J...",
  "createdAt": "2026-11-16T10:00:00Z",
  "expiresAt": "2026-11-16T10:05:00Z",
  "action": "buy",
  "instrument": "BTC/USDT",
  "size": {"mode": "notional", "value": "250"},
  "orderPreference": {"type": "limit", "tif": "gtc", "postOnly": true},
  "confidence": 0.71,
  "rationale": "Пробой диапазона на объёме, funding нейтральный, тренд 4ч вверх",
  "evidence": [
    {"kind": "indicator", "ref": "ema_200_1h", "value": "58210.5"},
    {"kind": "market", "ref": "volume_zscore_1h", "value": "2.3"}
  ],
  "invalidation": {"priceBelow": "57400", "timeStop": "2026-11-16T18:00:00Z"},
  "policyHints": {"maxSlippageBps": 8}
}
```

Обязательно: `rationale`, минимум одно `evidence`, `expiresAt`, `invalidation`.
Без этого риск-гейт отклоняет как `incomplete_proposal` (политика
`require_explanation` включена по умолчанию).

`size` — предложение, не приказ: финальный размер считает `sizing policy`
(risk-модуль) из лимитов, волатильности и уверенности.

Для decision-моделей «уверенность» — это концентрация распределения, а не
вероятность быть правым: `C = (p_max − 1/n) / (1 − 1/n)`. Порог в политике
ставится на `p_max` **исполняемого** варианта и отдельно на каждый вопрос;
`C` используется для сравнения моделей и калибровки.

## 5. Рантайм агентов

```
 event/cron ─▶ supervisor.Start(run)
                    │  проверка бюджета, лимитов, health провайдера
                    ▼
              build context (снапшот данных + хэши)
                    ▼
              provider call (LLM | Decision) ── timeout / circuit breaker
                    ▼
              validate output (schema, evidence, confidence)
                    ▼
              persist agent_run + decision_input + raw (redacted)
                    ▼
              emit agent.proposal ─▶ aggregator ─▶ RiskGate
```

- Параллельность: analyst/critic/venue-specialist могут идти одновременно;
  supervisor ждёт кворум или таймаут.
- Бюджеты: перед вызовом резервируется оценка стоимости; после — фактическая.
  Превышение → `budget_exceeded`, run продолжает работать без ИИ (fail-open для
  выходов, fail-closed для входов).
- Circuit breaker: 3 ошибки подряд → провайдер `degraded` на 60 с → fallback.
- Идемпотентность: `decisionId` = ULID, привязан к `(runId, eventSeq, agentId)`;
  повторный запуск на том же событии возвращает сохранённый результат (replay).
- Детерминированный реплей: сохраняются входные данные (снапшот), параметры,
  версия промпта и seed; для LLM реплей «как было» воспроизводит записанный ответ,
  «как сейчас» — новый вызов с пометкой `replay_diverged`.

## 6. Управление промптами

- Промпт — версионируемый артефакт (`prompts` таблица), а не строка в коде.
- Версия = хэш содержимого + SemVer; в `agent_versions` хранится ссылка.
- Изменение промпта требует прогона evals (см. §8) и не влияет на уже идущие
  runs (они пинят версию).
- Шаблоны: `system`, `task`, `output schema`, `few-shot`; переменные строго
  типизированы, неизвестная переменная — ошибка загрузки.
- В промпт не попадают секреты/PII (redactor), номера счетов маскируются.

## 7. Память и контекст

| Слой | Что хранит | Время жизни |
|---|---|---|
| `working` | снапшот рынка и портфеля для текущего решения | один вызов |
| `run` | последние N решений, позиция, P&L, ошибки | жизнь run |
| `instrument` | режимы рынка, уровни, статистика стратегии | дни/недели |
| `agent` | накопленные наблюдения роли (опционально) | версия агента |
| `org` | правила владельца, риск-политики, заметки | постоянно |

Память — явная и ограниченная по токенам (`textbudget`-подход из `lv24-brain`):
каждый слой имеет бюджет и приоритет вытеснения. Никакой «бесконечной истории
чата»: это дорого и ухудшает решения.

## 8. Калибровка и evals

Метрики на каждый прогон (сохраняются в `decision_outcomes`):

- `confidence` vs исход: **Brier score**, **log loss**, **ECE** (expected
  calibration error), reliability curve по бинам.
- Проверка контракта decision-модели: сумма вероятностей `1 ± 0.001`,
  argmax совпадает с `choice`, identity-residual мал, `p_max` ≥ порога.
- Counterfactual PnL отклонённых решений: что было бы, если бы пропущенный вход
  исполнился (оценка «дорого ли мы осторожничаем»).
- Точность по action-классам (buy/sell/hold), precision/recall для входов.
- Экономика: P&L после комиссий и слиппеджа, средний slippage, hit rate,
  profit factor — по агенту, модели, режиму рынка.
- Стоимость: $/решение, $/прибыльная сделка, токены, латентность p50/p95.

Evals как CI-гейт:
- `testdata/evals/<agent>/cases.jsonl` — записанные состояния + эталонные решения
  (не «правильные», а зафиксированные и размеченные).
- `vend agent eval <agent> --dataset cases.jsonl --baseline v1.1.0`
- Метрики сравниваются с baseline; деградация > порога — релиз агента блокируется.
- Для decision-моделей дополнительно: сумма вероятностей, margin top-2,
  распределение уверенности, доля `abstain`.

Это же — основа фичи «панель калибровки» в desktop: видно, можно ли доверять
уверенности модели, а не только её ответам.

## 9. Мультиагентная агрегация

Политики (детерминированные, выбираются в конфиге run):

| Политика | Правило |
|---|---|
| `single` | один владелец (стратегия или supervisor) |
| `quorum` | минимум K из N ролей за действие, иначе hold |
| `weighted` | сумма весов ролей × confidence ≥ порог |
| `veto` | любой `critic`/`risk` с `oppose` блокирует вход |
| `hierarchical` | supervisor решает, но обязан учесть veto-роли |

Тай-брейк: приоритет роли (`risk > critic > analyst`), затем `agentId`, затем
`proposalId` — результат воспроизводим.

Кворум обязан иметь таймаут: не дождались critic → действует политика
`on_timeout: abstain|proceed_without` (по умолчанию `abstain` для входов,
`proceed_without` для выходов).

**Гейт применяется только к входам.** Выходы, стопы, тейки и аварийное закрытие
не зависят от доступности ИИ и approve-очереди: сбой провайдера не должен
«запирать» позицию. Это же правило действует для pre-trade decision-гейта
(QuantDinger-паттерн: exit/SL/TP пропускают ИИ).

## 10. Безопасность ИИ

1. **Нет инструментов с деньгами.** У агента нет `place_order`; он возвращает
   `Proposal`. Инструменты — только чтение и вычисления.
2. **Prompt injection.** Внешний контент (новости, тикеры, on-chain) помечается
   как `untrusted` и подаётся в отдельной секции с инструкцией «данные, не
   команды»; инструменты агента не исполняют найденные в тексте инструкции.
3. **Схема выхода.** Свободный текст не парсится регулярками: только structured
   output (JSON schema/tool-call). Невалидный ответ = `invalid_output`, не
   «попробуем вытащить».
4. **Лимиты.** Токены, стоимость, частота, количество proposals, размер позиции —
   всё ограничено и проверяется кодом.
5. **Изоляция провайдера.** Агент не видит ключи; ключи не попадают в контекст;
   запросы идут через шлюз с allowlist моделей.
6. **Аудит.** Каждый вызов: кто (агент+версия), чем (модель+провайдер+промпт-хэш),
   на основании чего (хэш входа), что вернул, что решил риск.

## 11. «Кто за что отвечает»: матрица

```yaml
# config.yaml
agents:
  routing:
    - match: {venue: exchange-binance, assetClass: futures}
      roles: {analyst: crypto-futures-analyst, risk: portfolio-risk, executor: binance-executor}
    - match: {venue: broker-alpaca, assetClass: equity}
      roles: {analyst: equity-analyst, risk: portfolio-risk}
    - match: {assetClass: fx}
      roles: {analyst: fx-analyst}
  defaults:
    analyst: generic-analyst
    critic: generic-critic
    risk: portfolio-risk
```

Резолвер строит матрицу `role × venue × assetClass`; `vend agent matrix`
показывает покрытие и пробелы. Незаполненная критичная роль = run не стартует
(или стартует в режиме «только выходы»).

## 12. MCP-контроль (v1.1)

`vend mcp serve` поднимает MCP-сервер с безопасными инструментами:
`market.snapshot`, `account.summary`, `run.status`, `journal.query`,
`risk.status`, и с явно разрешёнными мутирующими (`run.pause`, `risk.kill`) —
по токену с правами. Это позволяет внешним агентам (Claude/Cursor/OpenCode)
управлять платформой, не получая доступа к ключам. Паттерн подсмотрен в
`jesse/mcp/server.py`.

## 13. Антипаттерны, которых избегаем

- Агент, который сам считает размер позиции «на глаз» — sizing только в policy.
- Промпт, зашитый в код, без версии и evals.
- Один «умный» агент на всё: нечего диагностировать, нечего заменять.
- Свободный текст вместо схемы; парсинг ответов регулярками.
- Бесконечная память чата; передача всей истории в каждый вызов.
- Молчаливый fallback на другую модель без записи в журнал.
- Общие синглтоны/реестры на процесс (мешает мультиарендности и тестам).
