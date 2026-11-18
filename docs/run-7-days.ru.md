# Семь дней непрерывной работы shadow-гейта

[English version](run-7-days.md)

Цель: накопить данные для критерия G1 — **≥ 7 дней работы и ≥ 100 размеченных
решений** (`docs/spec/10-roadmap.md`). M1 работает только в `shadow`: ордера не
отправляются, деньги не двигаются. Живые вызовы провайдеров идут только с
ключами владельца.

Этот документ — про длительный запуск, а не про быструю проверку. Быстрая
проверка на 15 минут — [runbook-live-smoke.ru.md](runbook-live-smoke.ru.md).
Если установки ещё нет, `vend init --quick` пишет профиль G1 (десять
USDT-символов на `15m`, обе стороны) и печатает два экспорта секретов — см.
[quickstart.ru.md](quickstart.ru.md). Обычный `init` оставляет `signals.trend`
выключенным и сам сигналов не даёт.

## 1. Почему `vend serve` в терминале, а не служба

Сейчас секреты читаются только из переменных окружения `VENDING_SECRET_*`
(см. [quickstart.ru.md](quickstart.ru.md), шаг 3). Файла с секретами и
ОС-keychain в M1 нет.

Службы `systemd`, `launchd` и планировщик Windows **не рекомендуются**, пока
секреты пришлось бы хранить в файле: `Environment=` в unit-файле, `EnvironmentVariables`
в plist или переменные в скрипте — это тот же секрет на диске. Готовых unit-файлов
в репозитории нет.

Поэтому основной способ — постоянная терминальная сессия:

- Linux / macOS: `tmux` или `screen`;
- Windows: отдельное окно PowerShell.

Компьютер не должен уходить в сон: при сне останавливается и HTTP-сервер, и
фоновый раннер. Настройте сон в ОС вручную (Windows: «Параметры → Система →
Питание и батарея → Экран и спящий режим»; macOS: «Системные настройки →
Энергосбережение»; Linux: настройки рабочего стола или
`systemd-inhibit --what=sleep:idle --why="vend shadow gate" vend serve ...`).
После недели верните прежние настройки.

Запуск в `tmux` (Linux/macOS):

```bash
tmux new -s vend
export VENDING_SECRET_CONN_DREX_APIKEY='<token>'
export VENDING_SECRET_CONN_LOCAL_APITOKEN='<token>'
# при включённом Telegram:
export VENDING_SECRET_CONN_TELEGRAM_BOTTOKEN='<token>'
vend serve --addr 127.0.0.1:8420
# отсоединиться: Ctrl+B, затем D; вернуться: tmux attach -t vend
```

Windows, отдельное окно PowerShell:

```powershell
$env:VENDING_SECRET_CONN_DREX_APIKEY = '<token>'
$env:VENDING_SECRET_CONN_LOCAL_APITOKEN = '<token>'
.\vend.exe serve --addr 127.0.0.1:8420
```

Окно не закрывать. `serve` слушает только loopback и запускает фоновый раннер:
разметку, trend-сигналы, уведомления и запись `shadow-status.json`.

## 2. Перезагрузка и остановка

Журнал лежит в каталоге данных (`vending.db`, `shadow-status.json`), конфиг —
отдельно (`vend config path`). После перезагрузки или `Ctrl+C` достаточно снова
задать переменные окружения и запустить `vend serve` с теми же `--config` /
`--data-dir` (или теми же `VENDING_CONFIG_FILE` / `VENDING_DATA_DIR`).

- журнал продолжается с последнего события, цепочка не начинается заново;
- `lastLabelAt` восстанавливается из последнего `outcome.labeled` при старте
  раннера;
- доставленные уведомления не повторяются: состояние алертов выводится из
  журнала (`notify.sent` с `uptoSeq`), поэтому тихий журнал остаётся тихим.

Проверка на `tools/smokestub` (локальная заглушка, без сети и реальных ключей;
Windows, 2026-11-16):

```bash
go build -o vend ./core/cmd/vend
go run ./tools/smokestub --addr 127.0.0.1:28471   # отдельный терминал
# в конфиге: providers.*.baseUrl = http://127.0.0.1:28471,
# вымышленные VENDING_SECRET_* и notify.telegram с baseUrl заглушки
vend serve --addr 127.0.0.1:8420
```

До остановки:

```text
events: 14
labeled: 1
lastLabelAt: 2026-11-16T20:15:52.9232211Z
notify.sent: 1 (provider_unavailable, uptoSeq=13)
```

После остановки `serve` и повторного запуска с теми же переменными:

```text
events: 14
labeled: 1
lastLabelAt: 2026-11-16T20:15:52.9232211Z
notify.sent: 1
journal verify: journal ok: 14 events
```

Счётчик событий не вырос, метка та же, повторного `notify.sent` нет. Команды
проверки:

```bash
vend status --json
vend journal verify
```

## 3. Ежедневная проверка

Раз в сутки, не останавливая `serve`:

```bash
vend status --json
vend journal verify
```

В `status --json`:

- `updatedAt` — время последней записи статуса; должен быть не старше
  `outcome.labelIntervalSec` плюс несколько секунд (по умолчанию 300 секунд);
- `labeled` — число меток; должно расти;
- `g1` — блок прогресса G1; `lastLabelAgeHours` должен оставаться меньше
  `expectedLabelIntervalHours`, а `labelsDue` — на нуле (раздел 4);
- `lastCycleError` — отсутствует или пустой; любой текст означает ошибку цикла;
- `lastLabelAt` — время последней метки;
- `policyHash` — 64 hex, тот же, что печатает `vend doctor`.

`journal verify` должен печатать `journal ok: N events`. Разрыв цепочки —
остановиться, сохранить каталог данных и сообщить, не продолжать запись.

Если `serve` остановлен, `updatedAt` в файле остаётся старым: свежесть
проверяется только при работающем процессе.

## 4. Прогресс G1: метки, дни, разрыв

`vend status` и `vend gate report` печатают один и тот же блок, построенный по
журналу, а не по `shadow-status.json`:

```text
g1: labels 23/100, active days 4/7, max gap 9.5h, eligible=no
last label age: 0.6h
expected label interval: 3.25h
labels due: 1
```

Те же числа есть в JSON, в объекте `g1`:

```json
"g1": {
  "labels": 23,
  "targetLabels": 100,
  "activeDays": 4,
  "targetDays": 7,
  "maxGapHours": 9.5,
  "eligible": false,
  "synthetic": false,
  "lastLabelAgeHours": 0.6,
  "expectedLabelIntervalHours": 3.25,
  "labelsDue": 1
}
```

В Windows PowerShell:

```powershell
(.\vend.exe status --json | ConvertFrom-Json).g1
```

С `jq`: `vend status --json | jq .g1`.

В JSON `lastLabelAgeHours: -1` значит, что меток ещё нет (в human-выводе
`never`); `labelsDue: -1` — окно неизвестно (`unknown`).

| Поле | Что значит |
|---|---|
| `labels N/100` | размеченные исходы выбранной политики. Backfill-метки не считаются никогда, даже с `--include-backfill`, поэтому с этим флагом число может быть меньше `labeled:`. |
| `active days D/7` | число разных UTC-дат с `gate.verdict`. ABSTAIN считается: гейт ответил. Дата, где был только `gate.request`, не считается. |
| `max gap Hh` | самая длинная пауза между двумя соседними отметками времени вердиктов. Она считается между ответами, а не между датами, поэтому семь дат с трёхдневной дырой не проходят. |
| `eligible=yes/no` | yes только при `labels >= 100`, `active days >= 7`, `max gap <= 36h` и несинтетическом журнале. |
| `last label age` | часы с последнего `outcome.labeled`. `never` — меток ещё нет. |
| `expected label interval` | `(horizonBars + 1) × timeframe`: 13h для значений по умолчанию (`12`, `1h`), 3.25h для быстрого профиля (`12`, `15m`). `n/a`, если в журнале нет размеченного исхода и конфиг не передан. |
| `labels due` | entry-сигналы, у которых окно разметки уже истекло, а ни `outcome.labeled`, ни `outcome.unlabelable` нет. `unknown` — окно нельзя вывести. |

`window: days=N active=M` — другая пара чисел: `active` считает UTC-даты с хотя
бы одним принятым `gate.request`, а G1 `active days` — даты с ответом. Неделя
запросов, на которые гейт не ответил, неделей работы не является.

### Что считать провалом

- **Меньше 100 меток к концу седьмого дня.** Последние решения седьмого дня не
  успеют разметиться: метке нужно `(horizonBars + 1)` баров после вердикта.
  Держите гейт запущенным, пока счётчик не дойдёт до 100; одной календарной
  недели недостаточно.
- **Меньше 7 активных дней.** Гейт должен ответить в семь разных UTC-дат, а не
  просто продержать журнал семь суток.
- **Разрыв больше 36 часов.** `eligible` останется `no` даже при 100 метках и
  семи датах: неделя не была непрерывной.
- **`eligible=no` по любой из причин выше или синтетический журнал.**
- **`labels due` больше нуля и растёт.** Столько entry-сигналов прошли окно
  разметки без метки и без `unlabelable`: гейт отвечает, а разметчик стоит (нет
  свечей, непустой `lastCycleError`, символ без истории). Сверьте с
  `awaiting labeling:` в отчёте.
- **`last label age` больше `expected label interval`.** Метки встали, даже
  если гейт продолжает отвечать. Проверьте `lastCycleError`,
  `providers.marketdata.baseUrl` и сеть, затем
  `vend journal tail --kind outcome.labeled` и `--kind outcome.unlabelable`.
  Полная таблица — в [troubleshooting.ru.md](troubleshooting.ru.md).
- **`last label age: never`.** Нормально до истечения первого окна; провал —
  только если прошли `expected label interval` плюс цикл раннера, а метки нет.

### Что не засчитывается

- **`vend demo`.** Любой журнал, в котором есть событие с `runId=demo`,
  считается синтетическим: счётчики G1 обнуляются (`g1: 0/100, 0/7`), блок
  печатает `SYNTHETIC — not G1 evidence`, `eligible` равен false, а
  `gate report --share` завершается кодом 2 с сообщением
  `synthetic journal is not G1 evidence`. Флага, который зачёл бы demo в G1,
  нет. Не используйте каталог demo для реального прогона: метка остаётся в
  этом журнале навсегда.
- **Backfill-метки.** Сигнал, пришедший позже чем через один таймфрейм после
  своего `ts`, помечается `backfill`; его метка по умолчанию исключается из
  счётчиков и сравнений, а отчёт печатает `backfill: labeled=N excluded=M`.
  `--include-backfill` добавляет такие метки в сравнение доходностей, но не в
  G1: счётчик меток G1 игнорирует backfill при любом флаге.
- **Офлайн-фикстура и replay.** `gate test` доказывает связку, а не рыночное
  поведение: за фикстурой нет свечей, меток она не даёт, а replay не создаёт
  нового решения.

## 5. Ориентир по объёму

100 меток за 7 суток — это в среднем **100 / 7 ≈ 15 меток в сутки**.

Перед началом недели проверьте, сколько сигналов даёт ваш конфиг
`signals.trend`:

```bash
vend signals estimate --days 7
```

Команда читает закрытые свечи настроенного marketdata-провайдера за последние
7 суток плюс прогрев EMA, считает пересечения тем же кодом, что и боевой скан,
и печатает строку на символ, итог и вердикт. В журнал она ничего не пишет,
decision-провайдера и Telegram не вызывает, секретов не требует.

Строка `SYMBOL error=invalid_request` означает, что история символа короче
окна (например, пара недавно появилась): уменьшите `--days` или уберите символ.

Добейтесь строки `verdict: enough`. Если сигналов мало:

- добавьте символы в `signals.trend.symbols` (каждый символ — свой поток
  сигналов);
- или уменьшите таймфрейм `signals.trend.timeframe` (например, `1h` → `15m`):
  на меньшем таймфрейме баров и пересечений больше;
- или подключите свой источник сигналов через `POST /v1/gate`.

### Оценка на реальном рынке

Владелец измерил частоту сигналов командой `vend signals estimate --days 7`
на релизном бинарнике `0.1.0-m1` и свечах Binance, 2026-11-17 16:44 UTC.
Это одна неделя одного рынка; прошлая частота не предсказывает будущую.

| symbols | timeframe | sides | signals per day | verdict |
|---|---|---|---|---|
| BTCUSDT | 1h | buy | 0.6 | low |
| BTCUSDT | 5m | buy | 4.9 | low |
| 5 symbols | 1h | buy | 1.9 | low |
| 5 symbols | 15m | buy | 11.4 | low |
| 5 symbols | 5m | buy | 27.7 | enough |
| 5 symbols | 15m | buy,sell | 22.9 | enough |
| 10 symbols | 1h | buy,sell | 8.3 | low |
| 10 symbols | 15m | buy | 21.7 | enough |

5 symbols = BTCUSDT, ETHUSDT, SOLUSDT, BNBUSDT, XRPUSDT. 10 symbols = те же
плюс DOGEUSDT, ADAUSDT, LINKUSDT, AVAXUSDT, LTCUSDT.

Один символ на `1h` порог не достигает. Для старта G1 нужен конфиг уровня
«10 символов на `15m`» или «5 символов на `5m`», и его надо проверить командой
`vend signals estimate --days 7` перед началом недели.

Каждый сигнал — это один вызов decision-провайдера, поэтому число вызовов
растёт вместе с числом сигналов: около 150 вызовов за неделю при 22 сигналах
в сутки. Цена одного вызова в этом документе не указана — неизвестно.

Пример только формата вывода, на `tools/smokestub` (Windows, 2026-11-16;
**свечи синтетические**): он показывает формат, а не объём рынка.

```text
BTCUSDT signals=8 per_day=1.1
total: signals=8 per_day=1.1
target: 15 per day (100 labeled in 7 days)
verdict: low
past frequency does not predict future frequency
```

В первые сутки сверяйте число **решений**, а не меток: строку `verdicts` в
`vend gate report`. Метка появляется через `(horizonBars + 1)` бара после
вердикта — при значениях по умолчанию это `(12 + 1) × 1h = 13` часов, — поэтому
в первые часы меток заведомо меньше, чем решений.

Фактическая частота `signals.trend` зависит от рынка: пересечение EMA fast/slow
на выбранном таймфрейме может не появиться ни разу за день. Гарантированного
числа сигналов в сутки нет; 15 — это ориентир, а не обещание.

Замер стоимости одного цикла раннера на журнале 50 000 событий (`go -C core test ./internal/app/shadow -run '^$' -bench BenchmarkRunnerCycle50kEvents -benchtime=1x`): 1.34 с — **замер на одной машине, не гарантия** (Windows 11, Intel Core Ultra 9 185H, 2026-11-17).

## 6. Что прислать на седьмой день

Только обезличенную сводку:

```bash
vend gate report --share
```

В ней нет символов, площадок, цен, размеров позиций, путей и идентификаторов;
агрегированная разница средней доходности ALLOW и REJECT с её 95% интервалом
включена. В сводке также есть строки G1 (`g1 labels`, `g1 active days`,
`g1 max gap hours`, `g1 eligible`, `g1 labels due`, `g1 last label age hours`,
`g1 expected label interval hours`). Синтетический журнал отклоняется: код 2 и
`synthetic journal is not G1 evidence`. Сводка публикуется вручную.

**Не присылать:** конфиг, ключи и переменные окружения, `vending.db` (журнал),
`shadow-status.json` с путями, скриншоты с ключами.

## 7. Что не проверено на этой машине

Команды и замер цикла раннера из этого документа проверены на Windows 11. Под
Linux на `vend_linux_amd64` в WSL Ubuntu запускались только `version`, `init` и
`doctor`; семидневный прогон, работа под сервис-менеджером и `tmux` там не
проверялись. macOS не запускался вовсе. Реальный семидневный прогон с живыми
Drex, Binance и Telegram тоже не выполнялся — только репетиция на
`tools/smokestub`. Частота сигналов измерена один раз за одну неделю (раздел 5);
полная неделя работы не повторялась, её стабильность неизвестна.
