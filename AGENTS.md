# AGENTS.md — правила работы в lv24-vending-machine

Нормативно для людей и ИИ-агентов. Спека: `docs/spec/`. Решения: `docs/adr/`.

## 1. Что строим

Платформа автоторговли: детерминированное Go-ядро (`core/`), CLI `vend`, демон
`vendd`, desktop (Tauri+React), web (React), mobile (Flutter). ИИ-агенты
совещательные: предлагают, но не исполняют.

## 2. Жёсткие запреты

- **Секреты** — никогда в коде, конфиге, БД, логах, тестах, коммитах. Только
  ссылки `conn/<id>/<field>` и OS-keychain.
- **Деньги** — не `float64`. Только `Decimal`; на границе — строки.
- **Обход RiskGate** — запрещён. Любой путь к бирже только через
  `OrderIntent → Evaluate → Reserve → OMS`.
- **`if provider == "..."`** в ядре — запрещено. Только capability-модель.
- **Ручная правка сгенерированного** (`*.gen.go`, `schema.d.ts`, Dart generated) —
  запрещена; правится схема в `contracts/`.
- **AGPL/GPL код** — нельзя копировать или линковать; только идеи (см.
  `docs/spec/09-research.md`).
- **Live-торговля** в тестах и CI — запрещена. Только sandbox/paper/mock.
- **Платные сервисы** (runners, SaaS, AI-аддоны) — запрещены.

## 3. Границы слоёв

```
domain → stdlib
app    → domain, ports
runtime→ app, ports, bus, journal
adapters/providers → domain, ports
api    → app
cmd    → всё (единственный composition root)
```

Проверка: `task lint:imports` (CI обязателен). Новый пакет — только с
обоснованием, зачем он не помещается в существующий слой.

## 4. Как добавить провайдера

1. `providers/<kind>-<vendor>/` — новый Go-модуль.
2. `provider.go`: `Descriptor()` (capabilities, config fields, деградации) +
   `Factory()`.
3. Реализовать узкие интерфейсы (`ExecutionProvider`, `CandleProvider`, ...).
4. `config.schema.json` — форма подключения для UI.
5. `conformance_test.go` — `porttest.Venue/MarketData/LLM/Decision`.
6. Добавить модуль в `go.work` и в `providers/registry_gen.go` (генератор).
7. Обновить `providers/catalog.yaml` и `docs/spec/04-providers.md` (матрица).
8. `task gen check && task test:providers`.

## 5. Как добавить агента

1. `agents/<role>/<id>/agent.yaml` — манифест по `contracts/jsonschema/agent/v1.json`.
2. Промпты — в `prompts/` с версией; никаких строк в коде.
3. Инструменты — только чтение/вычисления; никаких денежных операций.
4. Выход — `proposal/v1` с `rationale`, `evidence`, `expiresAt`, `invalidation`.
5. Evals: `testdata/evals/<id>/cases.jsonl` + baseline; `vend agent eval`.
6. Добавить в матрицу маршрутизации (`config.yaml → agents.routing`).

## 6. Как добавить стратегию

- Встроенная: `core/internal/strategy/builtin/<name>.go` + `StrategySpec`.
- Внешняя: WASM-пак (`pack.json` + `strategy.wasm`), ABI `v1`, без сети/ФС/часов.
- Обязателен parity-тест: одинаковые сделки в replay и paper.

## 7. Команды

```bash
task gen            # кодоген из contracts/
task gen:check      # drift-проверка (CI)
task check          # gen:check + lint + test + build
task test           # go test ./... -race + npm test + flutter test
task dev:desktop    # vite + tauri dev
task dev:web
task dev:core       # go run ./core/cmd/vendd
```

## 8. Definition of Done

- [ ] Контракты обновлены, `task gen:check` зелёный.
- [ ] Тесты: unit + conformance/parity там, где применимо.
- [ ] `task check` проходит локально.
- [ ] Нет новых секретов, зависимостей с AGPL/GPL, платных сервисов.
- [ ] Документация затронута (spec/ADR/README), если менялось поведение.
- [ ] Для риск/OMS/контрактов — ревью владельца.

## 9. Стиль

- Go: `gofumpt`, `golangci-lint`, ошибки типизированные, `context` первым.
- TS: `oxlint` + `oxfmt`, FSD-импорты только вниз, никаких `any`.
- Dart: `dart analyze --fatal-infos`, `dart format`.
- Коммиты: Conventional Commits; PR — маленький, с описанием риска и проверки.
