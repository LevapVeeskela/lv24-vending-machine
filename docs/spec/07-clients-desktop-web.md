# 07. Desktop и Web

Статус: draft v0.1 · Зависит от: `01-architecture.md`, `06-risk-execution.md`

## 1. Общая база

Desktop и web — одно приложение в двух оболочках:

```
packages/ui-web/          # FSD-слои, общие экраны, дизайн-система
  app/                    # провайдеры, роутинг, тема, i18n
  pages/                  # dashboard, runs, agents, risk, ...
  widgets/                # equity-chart, positions-table, decision-trace
  features/               # place-order, connect-provider, approve, kill-switch
  entities/               # run, agent, order, position, decision
  shared/                 # ui-kit, api-client, realtime, format, hooks
apps/desktop/             # Tauri-шелл + нативное
apps/web/                 # браузерная оболочка (тот же ui-web)
```

Стек: React 19, TypeScript 5.9, Vite 8, MobX 7 + `mobx-react-lite` (app-state),
`openapi-fetch` (server-state), React Router 7, `openapi-typescript` (типы из
контракта). Тот же набор, что в `lv24-brain-desktop`, — минимум новых решений.

Правила FSD: зависимости только вниз (`pages → widgets → features → entities →
shared`), публичный API слайса через `index.ts`, никаких импортов внутренностей.

## 2. Desktop: шелл и ядро

```
┌──────────────────────────── Tauri v2 (Rust) ────────────────────────────┐
│  native: tray, updater, deep-link, autostart, keychain, file dialogs,    │
│          notifications, single-instance, power/sleep events             │
│  ─────────────────────────────────────────────────────────────────────  │
│  CoreSupervisor: spawn sidecar `vendd`, dynamic port, session token,    │
│                  health-gate, restart with backoff, kill-tree on exit    │
│  ─────────────────────────────────────────────────────────────────────  │
│  WebView: React (packages/ui-web) ──HTTP/SSE──▶ vendd (127.0.0.1)        │
└──────────────────────────────────────────────────────────────────────────┘
```

Решения:

1. **Доменные данные — только через OpenAPI/SSE.** Tauri-команды остаются
   нативными (окно, трей, обновления, keychain, экспорт файлов). Дублировать
   доменный контракт в IPC нельзя: иначе desktop и web разъезжаются.
2. **Типизированный IPC** для нативных команд: `tauri-specta`/`ts-rs` генерирует
   TS-типы из Rust; ручной `invoke("...")` со строкой запрещён линтером.
3. **CoreSupervisor** (по образцу `tauri-plugin-sidecar` + `clash-verge-rev`):
   - динамический порт, per-session bearer token, передача через env/stdin;
   - health-gate перед показом данных; окно рисуется сразу (skeleton);
   - старт ядра в фоне с бюджетом (60 с), краш → тост + «перезапустить»;
   - на выходе `terminate()` ≤ 5 с, затем kill-tree; чистка сирот по `pid+exe`;
   - single-instance: второй запуск фокусирует окно и открывает deep-link.
4. **Обновления**: канал `stable|beta`, single-flight проверка, отложенная
   установка, подпись артефактов; `unsigned` сборки — только для внутренних тестов.
5. **Ключи** живут в ОС-keychain через ядро; webview их не видит. Формы
   подключения генерируются из `ConfigField` дескриптора провайдера.

## 3. Экраны v1

| Экран | Что показывает | Ключевые действия |
|---|---|---|
| **Dashboard** | equity, P&L день/неделя/месяц, экспозиция, режим PAPER/LIVE, свежесть данных, «требует внимания» | пауза всех run, kill switch, быстрый переход к проблеме |
| **Runs (боты)** | список запусков: стратегия, инструмент, P&L, статус, последний сигнал | start/pause/resume/stop, inspect, duplicate |
| **Run detail** | позиция, ордера, equity-кривая, решения (timeline), логи | изменить параметры с подтверждением, stop |
| **Agents** | роли, модели, бюджеты, здоровье, покрытие матрицы | включить/выключить роль, сменить модель, budget |
| **Decisions** | журнал решений: proposal → risk checks → approval → исход | открыть trace, «почему», фильтры, экспорт |
| **Positions/Orders** | открытые позиции и ордера, история филлов | cancel, close, flatten |
| **Markets** | watchlist, свечной график, индикаторы, стакан | добавить в run, создать алерт |
| **Backtests** | список, отчёт, equity, метрики, сравнение | запустить, сравнить, walk-forward |
| **Providers** | подключения, health, latency, capability-матрица | connect/test/disable, показать деградации |
| **Risk** | лимиты, использование, вердикты, shadow-diff | изменить лимиты (approve), kill switch, flatten |
| **Approvals** | очередь подтверждений с TTL и причиной | accept/reject с заметкой |
| **Settings** | модели и маршрутизация, бюджеты, режим, обновления, privacy | сохранить (аудит) |
| **Audit/Logs** | hash-chain статус, экспорт, системные логи | verify, export |

Глобально: `Cmd/Ctrl+K` (палитра команд), тосты, индикатор соединения,
i18n (ru/en), тема (dark по умолчанию + light), keyboard-first.

## 4. Safety-critical UI

- **Режим всегда виден**: `PAPER` (нейтральный) / `LIVE` (красный, persistent),
  плюс бейдж на трее/иконке.
- **DangerActionDialog** вместо `confirm()`: явные поля (инструмент, сторона,
  количество, цена, TIF), показ нотионала и лимита, typed confirmation для live,
  `commandId` для идемпотентности.
- **Kill switch** — в шапке, всегда доступен; fan-out по scope с `results[]`,
  частичный успех показывается честно (не «одно общее ok»).
- **Approve** — карточка с полным trace: кто предложил, на основании чего,
  какие проверки прошли, что будет при исполнении, TTL.
- **Расхождения** (`reconcile.mismatch`) — блокирующий баннер, пока не разрешено.
- **Секреты** — только write-only формы; после сохранения показывается маска и
  «последняя проверка», значение не возвращается никогда.

## 5. Realtime

- Один SSE-канал на клиент, комнаты: `run:<id>`, `account:<id>`, `agent:<id>`,
  `system`.
- Ref-count подписок: компонент подписался/отписался — канал не дублируется.
- Resume по `Last-Event-ID`; при разрыве — exponential backoff + индикатор
  «данные устарели».
- Коалесценция: тики рынка батчатся (16–50 мс), события ордеров — по одному.
- Fallback polling для окружений без SSE (редко), с явной пометкой.

## 6. Web

Тот же `packages/ui-web`, отличие — оболочка и режим:

| Аспект | Desktop | Web |
|---|---|---|
| Ядро | локальный sidecar | сервер `vendd` (multi-user) |
| Auth | локальный токен | OIDC/device-flow + сессии |
| Доступ | полный (владелец машины) | RBAC: viewer/trader/admin |
| Секреты | OS keychain | серверный secret store |
| Данные | SQLite | Postgres |
| Realtime | SSE локально | SSE за reverse-proxy |

Деплой web: статика (Vite build) + `vendd` за nginx/caddy; контейнеры в
`deploy/compose`. Никаких платных сервисов: self-hosted, PostgreSQL, без SaaS.

Web-специфика: устойчивость к reload (состояние в URL/кэше), deep links на
run/decision, shareable read-only ссылки (подписанный токен, TTL).

## 6.1 Графики

- **Рендерер:** `lightweight-charts` 5.x (Apache-2.0, ~62 КБ gzip) для web и
  desktop (один код, WebView2/WebKitGTK, WebGL не требуется).
- **Данные:** в окне держим 3–4k баров; агрегацию и прореживание делает ядро,
  клиент не пересчитывает историю.
- **Realtime:** `series.update(lastBar)` с коалесингом в `requestAnimationFrame`;
  полный `setData` на тик запрещён.
- **Индикаторы:** считаются в core (Go) инкрементально, клиент только рисует
  (единый источник для web/desktop/mobile/бэктеста).
- **Fallback:** `klinecharts` (27 индикаторов, 16 оверлеев, DataLoader), если
  нужны индикаторы/рисование «за спринт».
- **Лицензии:** LWC требует видимую атрибуцию TradingView (`attributionLogo`);
  `dxcharts-lite` — MPL-2.0 (file-level copyleft); Highcharts Stock, TradingView
  Advanced Charts, Syncfusion — не используем (платно/ограничения).

## 7. Производительность и качество

- Таблицы — виртуализация (10k+ строк).
- Бандл: initial JS < 350 КБ gzip; тяжёлые экраны — lazy.
- Web Vitals: LCP < 2.5 с, INP < 200 мс (desktop — быстрее, локальные данные).
- Тесты: vitest + Testing Library для фич, Playwright smoke для критичных
  сценариев (подключение, paper-ордер, kill switch, approve).
- Доступность: фокус-менеджмент, aria для таблиц/диалогов, контраст ≥ AA.

## 8. Интеграция с lv24 (позже)

Web-часть проектируется так, чтобы без переписывания стать разделом `lv24`:

- **Контракты совместимы**: REST `/api/v1/*` + SSE — тот же паттерн, что
  `lv24` realtime/assistant. Клиентские пакеты (`contracts-ts`, `ui-web`)
  публикуются как внутренние npm-пакеты и подключаются в `lv24/main/frontend`
  как отдельный route/микрофронтенд.
- **Auth**: в серверном режиме — OIDC через `lv24-identity`; локальный режим
  остаётся на device-token. Никаких собственных паролей.
- **Деплой**: тот же edge (nginx), путь `/api/vending/*` → `vendd`; статика —
  отдельный бандл. Никаких платных сервисов.
- **Изоляция**: `lv24` не импортирует внутренности vending; только контракты и
  UI-пакеты. Vending не зависит от `lv24` в локальном режиме.
- **Единый стиль**: i18n, темы, компонентная база — из `packages/ui-web`, чтобы
  визуально не отличаться от остального `lv24`.

## 9. i18n и форматы

- `i18next` + `react-i18next`; ключи в `shared/i18n/{ru,en}.json`; никаких
  строк в компонентах.
- Числа/даты/валюты — `Intl`; время в UTC с переключателем локального пояса.
- Decimal приходит строкой и форматируется без потери точности.

## 10. Антипаттерны (из ресёрча)

- Стор-монолит на тысячи строк (как `freqi/ftbot.ts`) — состояние по доменам.
- Глобальный WS-бродкаст на все экраны — только комнаты и подписки.
- `confirm()` для опасных действий и «общий ok» на массовые операции.
- Секреты или ключи в webview/локальном storage.
- Streamlit/дашборд-фреймворк в операционном UI.
- Отсутствие i18n и тёмной темы «потом» — это дешево сейчас и дорого позже.
