# M1 Phase B — активация и честный G1

Статус: принят к исполнению 2026-11-18 · Основание: аудит M1, Phase A
(`docs/plans/m1-audit-hardening.md`), ревью плана субагентом · Ветка:
`fix/m1-audit-hardening` (Phase A влит) · Условие старта: Phase A зелёный.

## 0. Цель

Сделать так, чтобы внешний человек мог за 5 минут получить первый вердикт, а
через 7 дней — честный ответ «сколько меток и сколько дней реально», не приняв
синтетику или дыру в работе за прогресс. Новых execution/UI-фич нет.

**Метрика:** G1 = ≥20 инсталляций × ≥7 дней непрерывной работы × ≥100
размеченных решений. Внутри одной инсталляции должны быть честно видны:
`labels N/100`, `active days D/7`, максимальный разрыв, возраст последней метки.

**Не цель:** HTML-отчёт, badge, installer, `gate send`, CSV/Parquet, Docker,
Telegram two-way, support bundle, demo в зачёт G1, любая торговля.

## 1. Принципы

- Синтетика и импорт не попадают в G1 ни при каком флаге.
- Счётчик G1 строится из журнала, а не из `shadow-status.json`.
- «7 дней» — это не только 7 разных дат: разрыв больше `MaxGap` означает
  «не непрерывно» и должен быть виден.
- Offline-проверка не имеет права писать «G1 готов»: объём сигналов
  проверяется только сетью (`vend signals estimate`).
- Секреты не пишутся на диск; `init --quick` печатает команды, а не значения.
- Всё аддитивно: старые конфиги и журналы читаются.

## 2. B1 — provenance, G1-прогресс, непрерывность

**Проблема.** Сейчас `status` показывает `labeled` и число событий, `report` —
`days` (span) и `active` (различные даты). 7 дат с трёхдневной дырой проходят
оба критерия. Нет способа отличить синтетический журнал от реального, поэтому
demo опасен для G1.

**Файлы.** `core/domain/report.go`, `core/domain/event.go` (только если нужно),
`core/internal/app/report/report.go` (+ тесты), `core/internal/cli/report.go`,
`core/internal/cli/status.go` (+ тесты), `core/internal/app/shadow/shadow.go`
(если status берёт данные оттуда), `core/internal/store/sqlite/store.go`
(только если нужен запрос; без изменения схемы).

**Требования.**
1. Новый блок `G1` в отчёте и в `status --json`:
   - `labels`, `targetLabels` (100);
   - `activeDays`, `targetDays` (7);
   - `maxGapHours` — максимальный интервал между соседними UTC-датами с
     активностью (или между событиями `gate.verdict`; выбери и документируй);
   - `eligible` — true только при `labels ≥ 100 && activeDays ≥ 7 &&
     maxGapHours ≤ 36`;
   - `synthetic` — true, если в журнале есть события с `runId == "demo"`;
   - `lastLabelAgeHours`, `expectedLabelIntervalHours` —
     `(horizonBars+1) × timeframe`, чтобы видеть «метки встали»;
   - `labelsDue` — сколько entry-сигналов просрочили окно разметки и не имеют
     ни `outcome.labeled`, ни `outcome.unlabelable` (если это дорого — вычисли
     из `awaiting` и возраста; главное — не врать).
2. Human `gate report` печатает строку:
   `g1: labels N/100, active days D/7, max gap Hh, eligible=yes/no`.
   `--share` печатает те же поля (без путей/символов).
3. `status` (human и `--json`) печатает G1-блок; `lastLabelAgeHours` и
   `expectedLabelIntervalHours` видны всегда, когда есть конфиг.
4. Синтетика:
   - `synthetic=true`, если есть события с `runId=="demo"`;
   - human/`status` печатают `SYNTHETIC` и `g1: 0/100, 0/7`;
   - `gate report --share` на синтетическом журнале — exit 2 с сообщением
     `synthetic journal is not G1 evidence`;
   - `gate report --json` отдаёт `synthetic: true`, но не засчитывает G1.
5. `MaxGap = 36h`; порог константой с комментарием. Если активность
   считается по `gate.verdict`, сигналы без вердикта (ABSTAIN) всё равно
   считаются работой гейта — это допустимо; объясни выбор в комментарии.
6. Тесты:
   - 100 реальных меток + 7 дат без разрыва → eligible;
   - 99 → нет; 100/3 дня → нет; 7 дат с 40-часовым разрывом → нет;
   - журнал с `runId=demo` → `synthetic`, G1 0/100, `--share` exit 2;
   - `status --json` содержит все поля; human-строка совпадает с JSON.
7. Не менять существующие поля `Report` (`Sufficient`, `Days`, `ActiveDays`)
   и не ломать golden-тесты.

**Приёмка.** `go -C core test ./internal/app/report ./internal/cli`.

## 3. B2 — `init --quick` и `doctor --strict`

**Проблема.** `init` создаёт `signals.trend.enabled=false, symbols=[]`; новичок
получает 0 сигналов и 0 меток за 7 дней. Это главная причина провала G1,
а не отсутствие HTML.

**Файлы.** `core/internal/cli/init.go`, `core/internal/cli/doctor.go`,
`core/internal/config/config.go` (только helper профиля, если нужен),
`core/internal/cli/cli.go` — **не трогать**, help-страницы интегрирует
оркестратор. Тесты: `cli/init_test.go` (если есть), `cli/doctor_test.go`.

**Требования.**
1. `init --quick`:
   - профиль: `signals.trend.enabled=true`, 10 символов (BTC, ETH, SOL, BNB,
     XRP, DOGE, ADA, LINK, AVAX, LTC в USDT), `timeframe=15m`, `fast/slow`
     из дефолтов, `sides=["buy","sell"]`, `outcome.labelIntervalSec=300`,
     `feeBps` из дефолта;
   - проверь `config.Validate`: 15m ≥ 2×300s — да;
   - печатает точные export-команды для bash и PowerShell (без значений),
     затем `config validate`, `doctor --strict`, `serve`;
   - `--json` — аддитивные `profile`, `nextSteps`; секретов нет;
   - `--force` работает как у обычного `init`.
2. `doctor --strict`:
   - offline-инварианты: config valid, trend enabled, ≥10 символов,
     timeframe 15m, label ≤ timeframe/2, `VENDING_SECRET_CONN_LOCAL_APITOKEN`
     и decision-secret заданы, БД открывается, policy hash есть;
   - **не** печатает «G1 ready»; при успехе пишет `profile ready; volume
     unverified` и следующую команду `vend signals estimate --days 7`;
   - exit≠0 при нарушении, с конкретным полем;
   - без `--strict` `doctor` ведёт себя как в Phase A.
3. Тесты: `init --quick` пишет валидный профиль; `doctor --strict` с dummy
   env → 0; без токена/с 5 символами/с trend off → ≠0; JSON-поля.
4. Не писать секреты в конфиг/данные/логи. Grep-тест на значения не нужен,
   но структура не должна содержать поля под ключи.

**Приёмка.** `go -C core test ./internal/config ./internal/cli`.

## 4. B3 — `vend demo`

**Проблема.** Первый offline-путь даёт `ALLOW=1` и `insufficient data`; ценность
продукта не видна без ключа и недели. Demo — единственный способ снять этот
барьер, но он не должен попасть в G1.

**Зависимость.** B1 (маркер `runId=demo`, G1-блок). Запускать после мержа B1.

**Файлы.** новый `core/internal/app/demo/demo.go` (+ тест),
`core/internal/cli/demo.go` (+ тест). `cli/cli.go` не трогать.

**Требования.**
1. `vend demo [--data-dir DIR] [--json] [--keep]`:
   - создаёт временный каталог (если `--data-dir` не задан), журнал с
     `runId="demo"`, 7 UTC-дней, ≥120 решений и ≥100 `outcome.labeled`;
   - синтетические вероятности и исходы детерминированы (фиксированный seed),
     Brier/ECE/AUC и counterfactual считаются реальным builder'ом;
   - печатает полный human-отчёт с баннером `SYNTHETIC — not real market data`
     в начале и в каждом крупном разделе (минимум: заголовок, метрики,
     assumptions);
   - `--json` — `synthetic: true` и те же числа; `--share` **не** поддерживается
     (exit 2, сообщение «synthetic journal is not G1 evidence»);
   - не вызывает провайдеров, не пишет за пределы demo-каталога, работает без
     сети/ключей; < 5 с;
   - без `--keep` удаляет каталог; `--keep` печатает путь и подсказывает
     `status --data-dir`.
2. `status --data-dir <demo>` показывает `SYNTHETIC` и `g1: 0/100, 0/7`
   (это обеспечивает B1; demo не должен обходить).
3. Тесты: детерминизм (два прогона — одинаковые числа), ≥100 меток, ноль
   вызовов провайдера, `--share` exit 2, каталог удаляется/сохраняется.
4. Не менять `report.go`/`status.go`; использовать их публичные функции.
   Если нужен новый рендер — только в `demo.go`.

**Приёмка.** `go -C core test ./internal/app/demo ./internal/cli` и ручной
прогон на чистой машине.

## 5. B4 — документация Phase B

**Файлы.** `README.md`, `README.ru.md`, `docs/quickstart*.md`,
`docs/run-7-days*.md`, `docs/troubleshooting*.md`, `docs/spec/03-core-cli.md`,
`docs/releases/0.1.0-m1.md`, `CHANGELOG.md` (оркестратор).

**Требования.**
- Quickstart: два пути — «посмотреть за 60 секунд» (`vend demo`) и «свой гейт
  за 5 минут» (`init --quick` → export → `doctor --strict` → `serve`).
- Run-7-days: G1-блок, `labels N/100`, `active days`, `max gap`, `labelsDue`;
  команда `vend status --json | ...` и что считать провалом.
- Demo: везде `SYNTHETIC`, не засчитывается в G1, `--share` отказывает.
- Troubleshooting: `doctor --strict` коды, «0 сигналов» (trend off / мало
  символов), «метки встали» (lastLabelAge).
- Все относительные ссылки живы; EN/RU синхронны.

## 6. Порядок и волны

```
B1 (G1/provenance) ─┬─▶ B3 (demo) ─┐
B2 (quick start)  ──┘              ├─▶ B4 (docs) ─▶ финальный прогон/dist
                                    ┘
```

- Волна 1: B1 и B2 параллельно, файлы не пересекаются.
- Волна 2: после мержа B1+B2 — B3 и B4 параллельно (B4 пишет по целевой
  спеке, B3 даёт фактические флаги; расхождения правит оркестратор).
- Финальная волна: полный `go -C core test ./...`, providers, e2e, Python,
  `gen:check`, `lintimports`, `dist/`, CHANGELOG.

## 7. Definition of Done (Phase B)

- [ ] `vend init --quick` на чистой машине даёт валидный G1-профиль и точные
      export-команды; секретов на диске нет.
- [ ] `vend doctor --strict` ловит trend off, <10 символов, короткий timeframe,
      отсутствие токена/ключа; не обещает G1 без сети.
- [ ] `vend demo` показывает ≥100 меток за <5 с, каждый раздел `SYNTHETIC`,
      `status` на demo — `0/100, 0/7`, `--share` отказывает.
- [ ] `status`/`report`/`--share` показывают `labels N/100`, `active days D/7`,
      `max gap`, `labelsDue`; 7 дат с 40-часовой дырой — `eligible=false`.
- [ ] Старые конфиги/журналы читаются; golden-тесты отчёта не изменились.
- [ ] Все тесты/e2e/линт/сборка зелёные; `dist/` пересобран и SHA сходится.

## 8. Риски

| Риск | Мера |
|---|---|
| G1-счётчик считает demo | `runId=demo` + `synthetic` + отказ `--share`; тест на 100 demo-меток → 0 |
| `max gap` ложно валит нормальный простой | порог 36 ч и явная документация; не менять `activeDays` |
| `doctor --strict` пугает offline-пользователя | отдельный флаг, без него поведение Phase A |
| demo выглядит как маркетинг | watermark в каждом разделе, фиксированный seed, никаких «доходность X%» без `SYNTHETIC` |
| demo пишет в реальный data-dir | только временный каталог; `--data-dir` явно; тест на отсутствие записи вне |
| быстрый старт пишет секреты | только печать команд; тест на структуру конфига |
| новые флаги ломают help | help-страницы интегрирует оркестратор после мержа |

## 9. Вне Phase B (backlog)

HTML/badge, `gate send`, installer, support bundle, CSV/Parquet, Docker,
Telegram two-way, historical import, read-only MCP, share-card PNG, hosted
bridge, автотюнинг, execution/OMS/UI. Публикация релиза — отдельное решение
владельца (zero-spend), не автоматизируется.
