# 10. План реализации

Статус: draft v0.1 · Зависит от: все предыдущие документы

## 1. Принципы планирования

- Каждый этап заканчивается **работающим срезом**, а не «половиной всего».
- Деньги и безопасность — раньше удобства: paper, risk gate, журнал и
  reconciliation появляются до первого live-ордера.
- Контракты и кодоген — в M0, иначе клиенты начнут дублировать типы.
- Live-режим — отдельный этап с чек-листом, а не «галочка в настройках».
- Никаких платных сервисов: публичный репозиторий, стандартные GitHub runners,
  self-hosted деплой.

## 2. Этапы

### M0 — Фундамент (1–2 недели)

| Работа | Результат |
|---|---|
| `contracts/`: OpenAPI + JSON Schema (common, proposal, risk, agent, events) | источник правды |
| Кодоген Go/TS/Dart + drift-тест | `task gen:check` зелёный |
| `core/domain`: Decimal, Instrument, Order, Proposal, RiskPolicy, ошибки | инварианты + тесты |
| `core/ports` + registry + capability-модель | реестр с валидацией дескрипторов |
| `core/store`: SQLite-миграции, journal append + hash-chain | `vend journal verify` |
| CLI: `init`, `doctor`, `version`, `config`, `provider catalog` | работает на Win/mac/Linux |
| CI: contracts, core, lint, path-фильтры | PR-гейт |

**Приёмка:** `vend init --template crypto-paper` создаёт конфиг и БД; `vend doctor`
проверяет окружение; `vend journal verify` проходит; контракты не дрейфуют;
бинарники собираются под три ОС.

### M1 — Данные, paper, риск (2–3 недели)

| Работа | Результат |
|---|---|
| `marketdata-ccxt` (REST) + `exchange-binance` (spot, REST+WS) | инструменты, свечи, стакан — первый рынок |
| `InstrumentRegistry`, `CandleService`, `BookStream`, `ConnectionSupervisor` | нормализация, gap-детект, реконнект |
| `paper` venue: fill model, комиссии, слиппедж, две книги | paper-исполнение |
| OMS: intent → reserve → submit → reconcile, идемпотентность | без дублей |
| RiskGate v1 (15 проверок) + YAML-политики + `risk simulate` | стабильные коды отказа |
| CLI: `market`, `account`, `order`, `risk`, `journal` | сценарий целиком в терминале |

**Приёмка:** grid-стратегия крутится 24 ч на живых данных в paper; 0 дублей
ордеров; reconciliation чистый после рестарта; `vend risk simulate` объясняет
каждую блокировку; реплей детерминирован.

### M2 — Стратегии и бэктест (2 недели)

- `Strategy` интерфейс + `StrategySpec` (параметры, границы, стороны) → из спеки
  генерируются CLI-флаги, формы UI и оптимизатор.
- Встроенные: `dca`, `grid`, `trend`, `rebalance`.
- WASM ABI v1 (`wazero`), `strategy validate`, лимиты памяти/топлива.
- Бэктест: виртуальные часы, paper-брокер, метрики, walk-forward, сравнение.
- **Parity-тест:** один и тот же пак даёт те же сделки в replay и paper.

**Приёмка:** `vend backtest run` на 2 годах данных < 60 с; отчёт с Sharpe/
drawdown/winrate; `vend backtest compare` показывает дельту; WASM-пак валидируется
и не имеет доступа к сети.

### M3 — Агенты и ИИ (3 недели)

- Манифесты агентов + реестр + рантайм (расписание, бюджеты, circuit breaker,
  конкурентность).
- Провайдеры: `llm-openaicompat`, `llm-anthropic`, `decision-drex`.
- Промпты как артефакты с версиями; память с бюджетами; redaction.
- Аггрегатор: `single|quorum|veto|hierarchical`, таймауты, `abstain`.
- Журнал решений + `agent explain` + метрики калибровки (Brier/ECE).
- Evals: датасеты, baseline, CI-гейт на деградацию.

**Приёмка:** связка analyst → critic → risk работает в paper; `vend agent explain`
показывает вход/модель/промпт-хэш/проверки/исход; бюджет останавливает вызовы;
недоступность провайдера не блокирует выходы; evals падают при регрессии.

### M4 — Desktop (3–4 недели)

- Tauri-шелл: sidecar-супервизия, typed IPC, трей, deep-link, updater.
- `ui-web`: dashboard, runs, run detail, agents, decisions, positions/orders,
  markets, providers, risk, approvals, settings, audit.
- Realtime SSE с комнатами и ref-count; графики LWC; индикаторы из core.
- Safety UX: PAPER/LIVE, kill switch, DangerActionDialog, approve-карточки.
- i18n ru/en, тема, `Cmd/Ctrl+K`, Playwright smoke.

**Приёмка:** полный paper-сценарий из UI без CLI; kill switch срабатывает и
виден; падение ядра восстанавливается супервизором; approve истекает по TTL.

### M5 — Live readiness (2–3 недели)

- Live-исполнение Binance/Bybit + user data stream.
- Approvals: desktop + Telegram; live-unlock с TTL и подтверждением.
- Reconciliation hardening: startup rebuild, sweep, orphan aging, дедуп филлов.
- `flatten`, экспорт аудита, алерты, метрики, дашборды.
- Чек-лист `06-risk-execution.md §13` как gate.

**Приёмка:** 2 недели paper без критических расхождений; live на минимальном
размере в пределах лимитов; ни одного ордера в обход gate (проверяется журналом);
kill switch и flatten проверены на реальном счёте с нулевой позицией.

### M6 — Web и Mobile (3–4 недели)

- Web: OIDC/device-flow, RBAC, Postgres, контейнерный деплой.
- Mobile: pairing QR, портфель, run pause/resume, approve, kill, push, оффлайн.
- Мультиустройство: список устройств, отзыв, «выйти на всех».
- Store-готовность: privacy policy, account deletion (Apple), Data safety (Google),
  возрастной рейтинг, экспортная декларация шифрования, RuStore-публикация.

**Приёмка:** approve с телефона доходит до исполнения < 5 с; push по критичным
событиям; отзыв устройства мгновенно блокирует доступ; оффлайн показывает
актуальность данных.

### M7 — Экосистема (дальше)

- Провайдеры: OKX, IBKR, MT5-мост, DEX-адаптер.
- Out-of-process провайдеры (gRPC) для Python/TS.
- Shadow-режим и replay-diff в UI; панель калибровки.
- MCP-контроль (`vend mcp serve`), marketplace паков, SDK.
- Multi-tenant SaaS (если решим продуктом).

## 3. Порядок и зависимости

```
M0 ─▶ M1 ─▶ M2 ─┬─▶ M3 ─▶ M4 ─▶ M5 ─▶ M6 ─▶ M7
                └───────────────▶ (Desktop можно начать после M1 на моках)
```

Критический путь: **M1 (данные + paper + риск)**. Всё остальное можно вести
параллельно после стабилизации контрактов.

## 4. Команда и параллелизация

| Поток | Кто | Когда |
|---|---|---|
| Контракты + core | владелец + агенты | M0–M1 |
| Провайдеры бирж | агенты (по одному на вендора) | M1, M5 |
| Стратегии + бэктест | агенты | M2 |
| Агенты + LLM | владелец (критично) + агенты | M3 |
| Desktop UI | агенты (FSD-слайсы) | M4 |
| Web/Mobile | агенты | M6 |

Правило: критичный код (risk, OMS, contracts, security) — только с ревью
владельца; остальное — по чек-листам и conformance-тестам.

## 5. Риски

| Риск | Вероятность | Влияние | Мера |
|---|---|---|---|
| Нестабильность API бирж, блокировки | высокая | среднее | несколько провайдеров, абстракция, sandbox |
| Расхождение бэктест/live | средняя | высокое | единый маппинг и paper-брокер, parity-тесты |
| Стоимость/латентность LLM | средняя | среднее | decision-модели, бюджеты, локальный fallback |
| Ошибка в риск-гейте | средняя | критическое | 15 проверок, property-тесты, shadow-mode, ревью |
| Утечка ключей | низкая | критическое | keychain, нет права вывода, redaction, аудит |
| Лицензионное заражение | средняя | высокое | allowlist лицензий; AGPL/GPL — только идеи |
| Рост scope | высокая | среднее | non-goals, milestone gates |
| Юридические ограничения (сигналы/управление) | средняя | высокое | не продаём сигналы, нет кастоди, дисклеймеры |
| Windows-специфика (пути, процессы, подпись) | средняя | низкое | CI под Windows, тесты супервизора |
| Выгорание/ресурс | средняя | высокое | маленькие срезы, автоматизация, агенты |

## 6. Критерии «v1 готов»

- [ ] Paper-режим: полный цикл из CLI, desktop, mobile.
- [ ] Минимум 2 биржи (live) + 1 брокер (paper/live).
- [ ] Risk gate + kill switch + approvals + reconciliation в проде.
- [ ] Агенты: LLM + decision-модель, объяснение каждого решения, бюджеты.
- [ ] Бэктест с parity и walk-forward.
- [ ] Журнал с hash-chain, экспорт, реплей.
- [ ] Документация: quickstart, runbook, security, provider SDK, agent SDK.
- [ ] CI зелёный, релизы подписаны (или явно unsigned), SBOM.
- [ ] 30 дней paper без критических инцидентов.
