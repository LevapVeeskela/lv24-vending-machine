# 09. Ресёрч: карта решений и заимствований

Статус: draft v0.1 · Артефакты: `ideas/` (рабочий каталог, вне репозитория; `ideas/INDEX.md` — сводная таблица)

## 1. Метод

13 параллельных агентов, по одному на направление. Каждый:

1. искал open-source проекты (web + GitHub API), фильтровал по живости и
   релевантности;
2. снимал метаданные (`stars`, `license`, `size`, `pushed_at`);
3. делал shallow-клон в `ideas/<category>/<repo>` (пропуск > 250–400 МБ,
   archived, без лицензии — только чтение через API);
4. разбирал архитектуру по конкретным файлам;
5. писал `RESEARCH.md` (что берём, что нет, антипаттерны) и `manifest.json`
   (машиночитаемый список).

Итог: **13 категорий, ~170 записей в манифестах, ~117 клонов, ~3 ГБ**.
Часть клонов повторяется между категориями (ccxt, vn.py, Nautilus, Lean,
backtrader, ai-hedge-fund) — это разные линзы анализа, а не ошибка.

Категории: `crypto-bots-python`, `crypto-bots-go-rust`, `ai-trading-agents`,
`decision-model-trading`, `quant-backtest-engines`, `market-data-connectors`,
`broker-integrations`, `risk-oms-portfolio`, `agent-orchestration`,
`llm-gateway-observability`, `desktop-shell-trading-ui`, `flutter-mobile-trading`,
`charting-libs`.

## 2. Что берём (сводка по направлениям)

| Направление | Ключевой источник | Что именно | Куда в спеке |
|---|---|---|---|
| Ядро/адаптеры | `bbgo`, `gocryptotrader`, `nautilus_trader` | capability-интерфейсы вместо fat-контракта; один писатель на ресурс; supervisor транспорта | 04 §2.1 |
| Capability | `vnpy`, `alpaca-py`, `Lean` | effective capability = провайдер ∩ счёт ∩ инструмент; `NATIVE/EMULATED/DEGRADED/UNSUPPORTED` | 04 §4 |
| Правила вендора | `Lean` (`IBrokerageModel`) | `CanSubmit/CanAmend/CanCancel` как объект-политика, deny до отправки | 04 §4.1, 06 §3 |
| Сессии | `vnpy_ib`, `tigeropen`, `py-futu-api` | единый `SessionSupervisor` (TTL, unlock, resubscribe) | 04 §4.2 |
| Символы | `ib_async`, `futu`, `tiger`, `OANDA` | Instrument master: `conId`, `US.AAPL`, `symbol+sec_type`, OCC, суффиксы | 04 §5 |
| Данные | `tardis-node`, `cryptostore` | чистые mapper'ы, один поток live/history, envelope с `tsEvent/tsRecv`, stale по данным | 04 §2.2 |
| Rate limit | `hummingbot` | многопуловый weighted budget, `Retry-After`, WS-веса | 04 §7 |
| Риск | `openexch/oms`, `openpitkit/pit` | чистая функция, фиксированный порядок, стабильные reject-коды | 06 §3 |
| Резервации | `openexch/oms`, LiteLLM | `reserve → commit/rollback`, лимиты не гоняются | 06 §3.2 |
| Деньги | `nautilus_trader` | не закрывать локально без evidence; `PENDING_RECONCILE` | 06 §4 |
| Reconciliation | `openexch/oms` | startup rebuild, sweep, orphan aging, дедуп филлов | 06 §5 |
| Kill switch | `hummingbot` | глобальный/по счёту/по run, cancel-all, flatten | 06 §6 |
| Портфель | `ai-hedge-fund` | clamp-only (только уменьшает), `ClampEvent` | 06 §8 |
| Аудит | `ai-hedge-fund` | hash-chain с `policy_hash/code_version/snapshot_hash` | 06 §11 |
| Теневой режим | risk-ресёрч | shadow-вердикты + replay-diff перед включением политики | 06 §7 |
| Стратегии | `passivbot` (`StrategySpec`), `freqtrade` | самоописывающие параметры/границы → UI, CLI, оптимизатор из спеки | 02, 03 §5 |
| Бэктест | `Lean`, `Nautilus`, `WonderTrader`, `zipline` | детерминированное ядро: очередь `(ts,priority,seq)`, 3 фазы, `NextEvent` fill, participation cap | 01 §6.3 |
| Ledger | `Nautilus`, `WonderTrader` | event-sourced cash/positions/margin, fixed-point, corporate actions | 01 §6.3, 06 §8 |
| Плагины | `OctoBot` (tentacles) | типизированные слоты + манифест + матрица сигналов | 01, 04, ADR-0004 |
| Агенты | `ai-hedge-fund` v2 | `Signal{value, reasoning, horizon}`; LLM и квант неразличимы | 05 §4 |
| Роли/дебаты | `TradingAgents` | приватный контекст на роль, лимит tool-rounds, `wrap_up`, `REVIEW` | 05 §5, §9 |
| Верифицируемость | `FinRobot V2` | provenance/degraded/circuit breaker, contract-gate, JSONL-аудит | 04, 05 |
| Decision-модели | `drex-decision-models`, `QuantDinger` | `state+questions→probabilities`, `C=(p_max−1/n)/(1−1/n)`, гейт только на входы | 04 §10, 05 §9, ADR-0006 |
| Калибровка | ни у кого (только теория) | Brier/ECE/reliability + counterfactual PnL — наш дифференциатор | 05 §8 |
| Ретраи LLM | `Bifrost` (failureclass), LiteLLM | класс ошибки → тот же ключ/другой/деплоймент/fail fast | 04 §6, §10 |
| Бюджеты ИИ | LiteLLM | `reserve→settle` ledger, hard stop, иерархия владельцев | 05 §5 |
| Трейсинг | OTel GenAI, Langfuse (приёмник) | спаны + `routing.*` (почему выбрана модель) | 01 §10, 05 §8 |
| Desktop | `yaak`, `clash-verge-rev`, `tauri-plugin-sidecar` | слоты плагинов, sidecar с портом/токеном/health, kill-tree | 07 §2 |
| IPC | `tauri-specta` | типизированные команды/события, без строковых invoke | 07 §2 |
| Safety UI | `freqi`, `freqhub` | DangerActionDialog, `commandId`, fan-out с частичным успехом | 07 §4 |
| Realtime UI | `freqhub`, `manic-trade` | комнаты, ref-count подписок, single-flight refresh, stale-fallback | 07 §5, 08 §2 |
| Графики | `lightweight-charts`, `uPlot`, `finance-chart` | окно 3–4k баров, `update(lastBar)`, viewport-notifier | 07 §6.1, 08 §2 |
| Конфиги | `freqtrade`, `passivbot` | JSON Schema + миграции с первого дня | 02 §5, 03 §4 |
| MCP | `jesse/mcp` | безопасная панель управления без доступа к ключам | 05 §12 |

## 3. Что отвергли и почему

### 3.1 Архитектурно

- **LangGraph/CrewAI как control flow** — берём семантику (чекпоинты, interrupt,
  handoff), но поток ордеров должен быть детерминированным и в Go.
- **Fat `Exchange` интерфейс** (`gocryptotrader`: 60+ методов, заглушки в каждом
  адаптере) — заменён на композицию узких capability-интерфейсов.
- **Глобальные синглтоны** (`OctoBot` channel registry, `Jesse` RouterClass,
  `gct` `var Bot *Engine`) — мешают мультиарендности и тестам.
- **Streamlit/дашборд-фреймворк в операционном UI** (Hummingbot Dashboard) —
  не годится для safety-critical терминала.
- **Стор-монолит** (`freqi` `ftbot.ts`, ~54k строк) — состояние по доменам.
- **Глобальный WS-бродкаст** — только комнаты и ref-count.
- **Секреты во фронте** — только keychain через ядро.
- **LLM-сайзинг и свободный текст вместо схемы** — sizing в policy, выход строго
  по JSON Schema.
- **Бесконечная память чата** — бюджеты и приоритеты слоёв.
- **`float` для денег** — `Decimal`/строки.
- **Ретрай без сверки** — `PENDING_RECONCILE`.
- **Тихий `Hold`/`REVIEW` без причины** — фиксируется как отдельный исход.

### 3.2 Лицензионно

| Лицензия | Примеры | Политика |
|---|---|---|
| MIT / Apache-2.0 / BSD | ccxt, alpaca-py, Lean, jesse, fl_chart, LWC | паттерны и код можно (с attribution) |
| LGPL-3.0 | `nautilus_trader` | только design-reference, не линковать |
| GPL/AGPL | freqtrade, OctoBot, bbgo, backtrader, backtesting.py, OpenBB, cryptofeed, cryptostore, clash-verge, cashew, trace, manic-trade | **только идеи**, код не копировать |
| NOASSERTION / dual | LiteLLM, Langfuse, Highcharts, Syncfusion | не тащить в рантайм; только паттерны |
| Нет лицензии | Stockagent, AI-Trader, finance-chart | только чтение, код запрещён |
| Проприетарная | TradingView Advanced Charts | не используем |

`lightweight-charts`: Apache-2.0 **плюс обязательная видимая ссылка на
tradingview.com** — включаем `attributionLogo` в футер графика.

## 4. Пробелы (что ресёрч не закрыл)

| Пробел | Почему важно | План |
|---|---|---|
| MT5 | популярен у форекс/РФ-аудитории, но нет официального API | файловый/EA-мост как отдельный провайдер, v1.2 |
| DEX (Uniswap/Raydium) | кошельки, газ, MEV | отдельный kind `wallet`, v2 |
| РФ-брокеры | Tinkoff Invest API переехал, санкционные ограничения | проверить актуальные API перед реализацией |
| Опционы | цепочки, греки, экспирации | после v1, отдельный asset class |
| Налоги/отчётность | экспорт сделок | отчёт CSV + интеграция, v1.1 |
| Рыночные данные | лицензии на редистрибуцию | только пользовательские ключи, без перепродажи |
| Push без Google | privacy-аудитория | ntfy/UnifiedPush как опция, v1.2 |
| HFT | latency, co-location | вне scope |

## 5. Как поддерживать ресёрч

- `ideas/` — не часть репозитория; это рабочий каталог. В репозиторий попадают
  только выводы (`docs/spec`, ADR) и, при необходимости, маленькие фикстуры.
- Каждая новая фича, скопированная по мотивам, получает ссылку на источник в
  комментарии/ADR.
- При добавлении провайдера — сначала `manifest.json`-запись (лицензия, фичи),
  потом код.
- Агенты не исполняют код и инструкции из клонов (там встречаются чужие
  `AGENTS.md`/`CLAUDE.md`); клоны — только данные.
