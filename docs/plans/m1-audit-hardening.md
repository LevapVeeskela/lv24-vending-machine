# M1 audit hardening — план и спека

Статус: принят к исполнению 2026-11-18 · Основание: аудит M1 (7 агентов,
`docs/review/` + отчёт сессии) · Ветка: `fix/m1-audit-hardening` (от
`fix/m1-stage-m-followup`, HEAD `2475974`) · Гейт: живой smoke владельца.

## 0. Цель

Закрыть P0/P1 аудита до живой проверки и публикации RC, не расширяя продукт.
После Phase A владелец может: собрать `dist/`, пройти runbook копипастом,
получить живые вердикты и метки, а отчёт не будет показывать статистику,
которой нет.

**Не цель:** demo, HTML-отчёт, installer, Telegram two-way, execution/UI.
Это Phase B (`§6`), только после успешного smoke.

## 1. Принципы

- Обратная совместимость: существующие конфиги, журналы и JSON-ответы
  продолжают работать. Новые поля — аддитивные.
- Никаких новых зависимостей и платных сервисов. Circuit breaker, limiter,
  BOM и разбиение Telegram — stdlib.
- Секреты не пишутся на диск, не логируются, не попадают в тесты.
- Каждая работа заканчивается тестом, который падает без фикса.
- `live` по-прежнему запрещён; ордеров нет.
- Контракты: `contracts/jsonschema/` — источник правды; `*.gen.go` не править
  руками, только `task gen` + `task gen:check`.

## 2. Phase A — работы

### A1. Доверие к отчёту

**Проблема.** `Sufficient` = ≥100 labeled, но калибровочных пар может быть 0:
печатаются `Brier=0`, `ECE=0`, `AUC=0.5`. Bootstrap CI не требует минимума на
группу: 1 ALLOW из 100 даёт «значимую» разницу.

**Файлы.** `core/domain/report.go`, `core/internal/app/report/report.go`,
`core/internal/cli/report.go` (+ их тесты), при добавлении поля —
`contracts/jsonschema/report/v1.json` и `core/contracts/contracts.gen.go`
через `task gen`.

**Требования.**
1. Ввести `MinCalibrationPairs = 30`. Если `CalibrationCount < 30`, метрики
   Brier/ECE/AUC/BSS/reliability не считаются валидными.
2. Явный признак в отчёте: `calibrationSufficient` (bool) в
   `ReportComparisons` и в JSON-схеме. Если регенерация контракта дороже
   0.5 дня — допустимо без поля, но тогда human/`--share` обязаны печатать
   `calibration pairs: N` и `n/a` вместо метрик, а порог документируется.
3. Human и `--share`: при недостатке пар — `brier: n/a`, `ece: n/a`,
   `auc: n/a`, `bss: n/a`, `calibration pairs: N (need 30)`. Никаких нулей.
4. `--share` всегда печатает `calibration pairs`.
5. `Difference` (bootstrap) выдаётся только при ≥30 наблюдений в каждой
   группе и ≥5 днях; иначе `insufficient group size` (или отсутствие блока),
   но не интервал из одного наблюдения.
6. `AUC` при пустом классе — `n/a`; `BSS` при base rate 0/1 — `n/a`.
7. `gate report` проверяет hash-chain перед отчётом: при ошибке — exit ≠0 и
   сообщение; флаг `--allow-broken-chain` оставляет текущее поведение.
8. Golden-тесты: 100 labeled/0 pairs; 1 ALLOW/99 REJECT; 30/30; нормальный
   отчёт не меняет прежние числа.

**Приёмка.** `go -C core test ./internal/app/report ./internal/cli`.

### A2. Идемпотентность `signalId` между процессами + replay

**Проблема.** Проверка `signalId` идёт по журналу до вставки; два процесса
(два `serve` на разных портах, `serve` + `gate test`) оба не находят запись и
делают два платных вызова и два решения. `gate test` при повторе молча
возвращает старый вердикт, игнорируя новый `--fixture`.

**Файлы.** `core/internal/store/sqlite/migrations/002_signal_id.sql` (новый),
`core/internal/store/sqlite/store.go` (+ тест), `core/internal/app/gateflow/flow.go`
(+ `flow_test.go`, при необходимости `arena_test.go`),
`core/internal/cli/gate.go` (+ тест).

**Требования.**
1. Миграция v2: колонка `signal_id TEXT` в `events`, заполняется для
   `gate.request`; уникальный частичный индекс:
   `UNIQUE(signal_id) WHERE kind='gate.request' AND signal_id IS NOT NULL`.
   `latestVersion = 2`, миграция идемпотентна, старые БД открываются.
2. `gate.request` пишется **до** вызова decision-провайдера (после валидации
   и проверки fingerprint). Существующий `complete` уже умеет дописывать
   недостающие события после краха — сохранить это.
3. Конкурентный проигравший получает уникальный конфликт, перечитывает
   журнал и возвращает replay победителя; если вердикта ещё нет — `409`
   (`CodeConflict`) с hint «retry», но **не** второй вызов провайдера.
4. Последовательный replay того же тела — как сейчас: `Reused=true`, без
   новых событий. Другое тело с тем же `signalId` — `409`.
5. `gate test` печатает `replayed: true` и hint. Если `--fixture` не совпадает
   с записанным `decision.response` — exit ≠0 с сообщением «fixture differs
   from journaled decision; change signalId or use another --data-dir».
   Одинаковый fixture — replay, exit 0.
6. BOM в `--request`/`--fixture` принимается; ошибка JSON остаётся внятной.
7. Тест: два параллельных `gate test` с одним `signalId` → ровно 1
   `gate.request`, 1 verdict, 1 вызов провайдера (счётчик в stub).

**Приёмка.** `go -C core test ./internal/app/gateflow ./internal/store/sqlite ./internal/cli`.

### A3. Drex: таймауты, retry, breaker

**Файлы.** `providers/decision-drex/provider.go` (+ тесты),
`core/internal/resilience/resilience.go` (+ тест), `core/internal/app/gateflow/flow.go`
только если нужен общий таймаут (согласовать с A2).

**Требования.**
1. Таймаут вызова ≥60 с (вендор рекомендует 60); gateflow не должен обрезать
   его до 20 с. Значение — константа с комментарием и ссылкой на доку.
2. На 429/503/529 читать `retry-after-ms` и `retry-after`, ждать в пределах
   бюджета (cap), затем отдавать типизированную ошибку.
3. Circuit breaker: N последовательных ошибок → open на T, half-open пробный
   запрос; параметры константами, тест на переходы. Без внешних библиотек.
4. Разбирать `error.type` вендора (сейчас ищется `error.code`);
   `details.vendorCode` заполняется.
5. Не логировать секреты и тела.
6. Тесты: timeout, 503+Retry-After, breaker open/half-open/close, error.type.

**Приёмка.** `go -C providers/decision-drex test ./...` и
`go -C core test ./internal/resilience` (если менялся).

### A4. Binance и Telegram: границы и лимиты

**Файлы.** `providers/marketdata-binance/provider.go` (+ тесты),
`providers/notify-telegram/provider.go` (+ тесты).

**Требования.**
1. Binance: валидировать interval по enum (case-sensitive), `1H` — ошибка;
   `BTC/USDT:USDT` (фьючерсный суффикс) — явная ошибка «futures not
   supported», без молчаливого перехода на спот.
2. Binance: weight-budget (klines weight=2, лимит запросов в минуту),
   учитывать `Retry-After` на 429/418, не долбить при 418.
3. Binance: пагинация не обрезает молча; при достижении лимита итераций —
   ошибка с указанием запрошенного диапазона.
4. Telegram: проверять/разбивать текст по 4096 символов (по границе строк,
   не рвать UTF-8); `ok:false` — ошибка; 400 маппить в `invalid_request`;
   `parse_mode` не задавать, если нет экранирования (или экранировать).
5. Тесты на каждый пункт; без сети.

**Приёмка.** `go -C providers/marketdata-binance test ./...`,
`go -C providers/notify-telegram test ./...`.

### A5. Конфиг, `doctor`, `serve`, планировщик trend

**Файлы.** `core/internal/config/config.go` (+ тест),
`core/internal/cli/doctor.go` (+ тест), `core/internal/cli/serve.go`,
`core/internal/cli/serve_test.go`, `core/internal/app/shadow/shadow_test.go`.

**Требования.**
1. `config validate` парсит `signals.trend.timeframe` через
   `domain.ParseTimeframe`; `1x` — ошибка. Валидирует venue/symbols/URL как
   сейчас; неизвестный provider id в `config validate` не проверяется
   (реестр недоступен) — это делает `doctor`.
2. `doctor` выходит ≠0 при неизвестном `providers.decision.id` и при
   отсутствии `VENDING_SECRET_CONN_LOCAL_APITOKEN`; сообщение называет
   переменную. `doctor --live` не ломается.
3. `serve.shadowSignals` прокидывает `Venue` и `LabelIntervalSec` из конфига;
   guard «timeframe ≥ 2×label» начинает работать.
4. Тесты `nextTrendDelay`: ровно на закрытии, +1 с, середина бара, ровно в
   момент fire, interval<=0, offset 0; тест `Run` с коротким interval/offset
   (≤3 с) и проверкой, что скан происходит после закрытия, а не в цикле.
5. Исправить ложный комментарий `trendOffset` («tests shorten it» —
   сокращать можно поле, не const) или сделать поле тестируемым явно.
6. BOM в конфиге: срезать и подсказать «file has BOM» (если не сделано в A2).

**Приёмка.** `go -C core test ./internal/config ./internal/cli ./internal/app/shadow`.

### A6. CLI DX

**Файлы.** `core/internal/cli/cli.go`, `init.go`, `journal.go`, `status.go`,
`config.go`, `outcomes.go` (+ тесты). Не трогать `report.go` (A1) и `gate.go` (A2).

**Требования.**
1. `--help`, `-h`, `help`, `help <cmd>`, `<cmd> --help` — exit 0 и справка;
   список команд и флагов соответствует коду. Убрать `unknown subcommand`
   для `gate --help`.
2. `CodeConflict` (HTTP 409) → exit 4 и `code: conflict` во всех командах;
   убрать двойной префикс `vend: conflict:`.
3. `status --json` до `serve`: не печатать `"0001-01-01T00:00:00Z"` —
   `omitempty`/null для `updatedAt` и пустой `policyHash`.
4. `journal tail --kind <unknown>` — exit 2 с списком допустимых kind.
5. `init` печатает next steps: `config validate`, `doctor`, `gate test`,
   `serve`; путь к конфигу и data-dir.
6. `--out`/`--save-raw`: отказ, если файл существует, без `--force`;
   подсказка. Не перезаписывать `vending.db`/конфиг. Реализация — после A1/A2,
   чтобы не конфликтовать: helper в `cli.go`, call sites в `report.go`/`gate.go`
   добавляет соответствующий агент (A1/A2) или интеграционная волна.
7. BOM в JSON-лоадерах, которые остались у CLI.

**Приёмка.** `go -C core test ./internal/cli` (кроме тестов A1/A2, они зелёные
после своих работ).

### A7. Документация

**Файлы.** `README.md`, `README.ru.md`, `docs/quickstart*.md`,
`docs/runbook-live-smoke*.md`, `docs/install*.md`, `docs/troubleshooting*.md`,
`docs/run-7-days*.md`, `docs/releases/0.1.0-m1.md`, `SECURITY*.md`,
`docs/release-checklist.md`, `docs/spec/10-roadmap.md`, `AGENTS.md`,
`LICENSE-docs` (новый, если решено).

**Требования.**
1. Runbook: добавить `"state":{...}` в пример `curl`; проверить, что запрос
   проходит локально.
2. Quickstart/README (EN/RU): Windows — `go build -o vend.exe` и `.\vend.exe`;
   убрать утверждение, что файл без расширения запускается.
3. `install.md`/README/релиз: согласовать Linux (WSL `version/init/doctor`
   пройден, остальное нет) и macOS (не запускался); убрать «source build
   prints dev» — в этом дереве печатает `0.1.0-m1`.
4. `--share`: формулировка «no return amounts» → «no symbols, venues, prices,
   position sizes; aggregate return statistics included».
5. `SECURITY.md`: scope только M1 (API, журнал, секреты, провайдеры); убрать
   RiskGate/live/kill switch/approve/WASM/desktop/mobile.
6. `docs/releases/0.1.0-m1.md`: убрать `TODO(owner)`; заменить на факт или
   нейтральную фразу.
7. `AGENTS.md` §3/§8 и roadmap M0: CI не включён осознанно (zero-spend),
   проверки локальные; если CI появится — только бесплатный, без артефактов.
   Не противоречить `docs/release-checklist.md`.
8. `LICENSE-docs` (CC-BY-4.0) или явная ссылка; добавить в README.
9. Проверить все относительные ссылки и пары EN/RU; 0 битых.

**Приёмка.** Скрипт проверки ссылок; `git diff --check`; выборочно выполнить
команды из quickstart/runbook на Windows.

### A8. Тесты, Taskfile, e2e

**Файлы.** `Taskfile.yml`, `.gitignore`, `tools/e2e/*` (только если нужно),
`docs/release-checklist.md` — согласовать с A7.

**Требования.**
1. `task test:core` — быстрый: `-short` или `-timeout 30m`; 7-дневный тест
   вынести в `test:7d`.
2. `task test:e2e` — `VEND_E2E=1 go -C tools test -count=1 ./e2e/...`;
   `task check` не обязан включать e2e (он медленный), но `check:full` —
   обязан.
3. `task test:race` — цель с понятной ошибкой, если нет CGO; документация
   prereq (mingw-w64) или отказ от обещания в AGENTS §7.
4. `.gitignore`: `/vend`, `/vend.exe`, `/serveF.log` (если не отслеживается),
   `__pycache__/`, `*.pyc`.
5. Проверить, что `task check`-эквивалент проходит на Windows без `task`
   (команды из release-checklist).
6. Не менять поведение e2e; только таймауты/цели.

**Приёмка.** Ручной прогон команд; `go -C tools test ./...`.

### A9. Интеграция и релиз (оркестратор, после A1–A8)

1. Полный прогон: `go -C core test ./... -timeout 30m`, providers, tools,
   Python, `gen:check`, `lint:imports`, e2e.
2. Влить `fix/m1-stage-m-followup` (уже в ветке) и все работы; конфликты —
   вручную.
3. Пересобрать `dist/`, сверить `SHA256SUMS`, проверить `version`.
4. `CHANGELOG.md` — секция Unreleased с P0/P1.
5. Локальный тег `v0.1.0-m1` — только после зелёного прогона; push/публикация
   требуют явного разрешения владельца (zero-spend).

## 3. Трассировка к аудиту

| Находка | Работа |
|---|---|
| Runbook без `state` → 400 | A7 |
| Windows quickstart `.\vend` | A7 |
| `Sufficient` без calibration pairs | A1 |
| Bootstrap CI из 1 ALLOW | A1 |
| Дубли `signalId` между процессами | A2 |
| `gate test` игнорирует fixture | A2 |
| Планировщик trend 0% | A5 |
| 7d тест > 10m | A8 |
| Drex timeout/Retry-After/breaker | A3 |
| Binance interval/symbol/weight | A4 |
| Telegram 4096/ok:false | A4 |
| `config validate` неглубокий | A5 |
| `serve` не прокидывает venue/label | A5 |
| `--out` перезаписывает | A6/A9 |
| 409 → exit 1 | A6 |
| `--help` exit 2 | A6 |
| BOM | A2/A5/A6 |
| Отчёт на битой цепочке | A1 |
| `status` нулевые даты | A6 |
| Доки: Linux/dev/share/SECURITY/CC-BY/TODO | A7 |
| CI-противоречие | A7 |

## 4. Definition of Done (Phase A)

- [ ] Все A1–A8 приняты, тесты добавлены и падают без фикса.
- [ ] `go -C core test ./... -timeout 30m` — зелёный (без чужих нагрузок).
- [ ] `go -C providers/... test ./...` — зелёные.
- [ ] `VEND_E2E=1 go -C tools test -count=1 ./e2e/...` — зелёный.
- [ ] `python -m unittest discover -s examples` — зелёный.
- [ ] `go -C core test ./contracts -run TestGeneratedContractsMatch` — зелёный.
- [ ] `go run ./tools/lintimports` — зелёный.
- [ ] Ручной сценарий: два параллельных `gate test` → одно решение;
      repeat → `replayed: true`; reject fixture → внятная ошибка/новое решение
      по инструкции; runbook curl → 200.
- [ ] Отчёт: 0 пар → `n/a`, 1 ALLOW → нет «значимой» разницы.
- [ ] `git diff --check`; нет секретов; нет новых зависимостей.
- [ ] CHANGELOG обновлён; docs не противоречат коду.
- [ ] `dist/` пересобран из HEAD; SHA сходится.

## 5. Риски и откат

| Риск | Мера |
|---|---|
| Миграция v2 ломает старые БД | Идемпотентная DDL, тест на БД v1 + открытие; бэкап `vending.db` перед прогоном |
| Перенос `gate.request` до провайдера меняет порядок событий | Golden-тест порядка; replay/краш-тесты; вердикт по-прежнему после ответа |
| Отчёт начнёт скрывать полезные данные | Порог 30 пар только для метрик; counterfactual остаётся при ≥100 labeled |
| Провайдеры ломают фикстуры | Тесты на существующих фикстурах; новые кейсы — отдельно |
| Параллельные агенты конфликтуют | Жёсткие списки файлов; никаких `git add/commit`; интеграция A9 |
| CI-политика | Не включать CI в Phase A; только привести документы к решению |

## 6. Phase B — после живого smoke (не начинать сейчас)

Филлер-фичи для G1, по убыванию ценности:

1. `vend demo` — синтетическая неделя, полный отчёт, watermark, отдельный
   data-dir, не засчитывается в G1.
2. `init --quick` + `gate send` + `doctor --strict` — первый вердикт ≤5 мин;
   секреты не пишутся на диск.
3. `gate report --html` + `--badge` + G1-прогресс `N/100, D/7` в `status` и
   weekly Telegram.
4. Installer `install.ps1/sh` + Scoop/Homebrew/WinGet.
5. `--notional`, `export --csv/--jsonl`, `support bundle` (redacted),
   Telegram two-way `/status /report /mute`, шаблоны Python/CCXT/Node/n8n,
   Docker/compose, исторический импорт с защитой от look-ahead, read-only MCP,
   share-card PNG.

**Anti-features:** hosted-free/TradingView-мост, автотюнинг порогов на тех же
100 метках, live/OMS/ключи биржи до G1, desktop/mobile UI сейчас, зачёт
demo/import/backfill в G1, мультиагентные LLM-роли, бэктест-движок, WASM.

## 7. Вне Phase A (P2, отдельный backlog)

- `secretRef` namespace (`conn/<provider>/...`), Host-проверка и rate-limit
  на 401, ACL на Windows, retention/prune + VACUUM, внешний anchor журнала.
- Контракты: `sum(p)≈1`, `choice==argmax`, `if/then`, `additionalProperties`,
  OpenAPI для `/v1/gate`.
- Binance/Drex/Telegram P2 из аудита (images, noul criteria, error.type,
  usage required).
- `entryLagMs` семантика «сигнал→вход», неопределённые AUC/BSS в JSON.
- `-race` на Linux/CI, SBOM, подпись.
