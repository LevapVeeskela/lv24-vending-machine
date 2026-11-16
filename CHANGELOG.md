# Changelog

Формат — [Keep a Changelog](https://keepachangelog.com/ru/1.1.0/),
версии — [SemVer](https://semver.org/lang/ru/). Релизы пока локальные:
`0.1.0-m1` — релиз-кандидат, **не опубликован**; артефакты собираются
`go run ./tools/release`, публикации нет.

## [0.1.0-m1] — 2026-11-16 (релиз-кандидат, не опубликован)

Первый клин: shadow-гейт, который работает без присмотра, и офлайн-проверка
журнала. Ордеров нет, live отклоняется конфигом; живые вызовы провайдеров —
только ключом владельца.

### Added

- `vend init|config|doctor|version` и JSON-конфиг `vending.lv24/v1`
  (режимы `shadow|paper|sandbox`, `live` отклоняется).
- `POST /v1/gate` на `127.0.0.1:8420` с bearer-токеном
  (`VENDING_SECRET_CONN_LOCAL_APITOKEN`), лимитом тела 64 КиБ, 8 параллельными
  вычислениями, таймаутами и graceful shutdown; `GET /healthz`.
- Gateflow: журнал `gate.request → decision.response → decision.model_changed →
  gate.verdict`, идемпотентность по `signalId`, 409 на другое тело, ABSTAIN при
  ошибке провайдера.
- `vend gate test --request ... [--fixture ...] [--save-raw ...]` — офлайн-реплей
  записанного ответа со строгой проверкой по вопросам гейта.
- Разметка исходов: `outcome.horizonBars`, `outcome.feeBps` (за сторону),
  `receivedAt`, флаг `backfill` для поздних сигналов, `candlesHash`,
  `vend outcomes label [--candles FILE]`.
- shadow-раннер в `vend serve`: цикл разметки/сигналов/уведомлений каждые
  `outcome.labelIntervalSec`, атомарный `shadow-status.json`, ошибки цикла не
  останавливают работу; `vend status [--json]`.
- Отчёт `vend gate report`: счётчики, policy hash, backfill, стоимость, latency
  p50/p95, группы all/ALLOW/REJECT/ABSTAIN, bootstrap CI (интервал требует
  ≥ 5 различных дней входа), Brier, BSS, AUC, ECE, кривая надёжности, разбивка
  по моделям; флаги `--policy`, `--include-backfill`.
- `vend gate report --share [--out FILE]` — обезличенная markdown-сводка
  (агрегаты и метрики, без символов/цен/путей/идентификаторов), файл 0600.
- `signals.trend`: локальный источник EMA fast/slow по закрытым свечам,
  детерминированный `signalId`, настраиваемые symbols/timeframe/sides.
- `notify.telegram`: смена модели, 3 подряд ABSTAIN от недоступного провайдера
  (не чаще раза в час), недельная сводка counts; событие `notify.sent`,
  локальный лимит одно сообщение в минуту.
- Арена моделей: конфиг `arena`, отдельный вызов второго decision-провайдера
  с дедлайном 20 с вне пути ответа, событие `arena.decision`, агрегация
  (согласие, средний доход ALLOW, AUC, стоимость) в отчёте.
- Локальный релизный инструмент `tools/release`: 5 целей
  (windows/linux/darwin), `CGO_ENABLED=0`, `-trimpath`, `dist/SHA256SUMS`.
- `vend doctor --live [--symbol SYMBOL] [--save-raw FILE]` — опциональные
  сетевые пробы decision/candles/telegram с дедлайном 20 с; печатает `ok` или
  короткий код ошибки, сырой ответ decision пишется атомарно с правами 0600.
- Каноническая форма символа `domain.CanonicalSymbol`: `BTC/USDT`, `BTC-USDT`,
  `BTCUSDT` и `BTCUSDT:USDT` сравниваются как `BTCUSDT`; trend канонизирует и
  дедуплицирует символы, разметчик сравнивает свечи канонически.
- Контракт исхода: `entryLagMs` (задержка от open входного бара до момента
  доступности сигнала) и `entryShifted` (вход сдвинут на следующий бар);
  событие `outcome.unlabelable {signalId, decisionId, reason}` и счётчик
  `unlabelable` в `vend outcomes label`.
- Отчёт: сравнения только по последней паре `(horizonBars, feeBps)`, метки с
  другой парой — в `otherLabelParams` (JSON); допущения включают entry lag
  median/p95 (доля бара) и число сдвинутых входов.
- Документация: `docs/install.md` / `docs/install.ru.md`,
  `docs/runbook-live-smoke.md`, `docs/release-checklist.md`; обновлены
  `docs/quickstart.md` (EN), `docs/quickstart.ru.md` (RU), `README.md`,
  `docs/spec/03-core-cli.md`, `docs/spec/README.md`, рецепты `docs/recipes/`
  и Python-клиент гейта.

### Changed

- Конфиг: `outcome.feeBps` по умолчанию 10 за сторону (20 round-trip),
  добавлены `outcome.labelIntervalSec`, `notify.telegram`, `signals.trend`,
  `arena`; валидация требует конкретных id моделей и ссылок на секреты.
- Гейт возвращает фактический `mode` из конфига и плоский wrapper
  `{mode, signalId, verdict}`.
- `journal verify`/`head` принимают общие `--config/--data-dir/--json`.
- Отчёт отделяет backfill-метки от обычных и печатает `insufficient data`
  до 100 размеченных исходов.
- Код ошибки провайдера в вердикте: `provider_auth`, `provider_misconfigured`,
  `provider_unavailable`, `invalid_output`; вердикт остаётся `ABSTAIN`, но
  причина различима. Для auth/misconfigured причина обобщена: текст провайдера
  с токеном или телом запроса в журнал не попадает.
- `--save-raw` и запись конфига атомарны (temp-файл + rename, права 0600).
- `bss: n/a`, когда base rate 0 или 1: константный прогноз идеален, skill score
  не определён.
- Расписание уведомлений выводится из журнала: недельная сводка — не раньше
  7 дней после первого `gate.request` и 7 дней после последней успешной;
  курсор не двигается, пока уведомление не доставлено.
- Разбор таймфрейма — единая `domain.ParseTimeframe` для разметчика, trend,
  shadow и отчёта.
- `--share` включает версию, префикс policy (8 hex), число решений и UTC-дней,
  entry lag median/p95 и число сдвинутых входов.
- Статус документации: `0.1.0-m1` — релиз-кандидат, не опубликован; бинарники
  не подписаны.

### Fixed

- Разметка запрашивает свечи от начала бара, в котором лежит якорь, поэтому
  поздний сигнал размечается с первого запроса; порог переноса входа —
  `min(10% интервала, 30 секунд)`, без запасного бара сигнал ждёт следующего
  цикла, а «пропуск»/разрыв ставит его на паузу 1, 2, 4 … интервала (не реже
  раза в час) с одним предупреждением на сигнал. Перед `outcome.unlabelable`
  по сроку делается одна попытка разметки.
- Уведомления о сбое провайдера строятся по журналу отдельно для
  `provider_auth` и `provider_misconfigured` и не повторяются без нового
  вердикта; недоставленная отправка повторяется с паузой 1, 2, 4 … цикла
  (не реже раза в час), а `notify.sent` с `ok: false` пишется только при первой
  неудаче серии. В `notify.sent` добавлены необязательные `code` и `uptoSeq`.
- `vend gate report` и `--share` печатают `awaiting labeling`,
  `no outcome by deadline` и `source rejected`; entry lag считается только по
  исходам с записанным `entryLagMs` и печатается в долях бара и в секундах.
- `vend doctor --live` пропускает ответ модели через `gate.Evaluate` и печатает
  `decision: ok verdict=... code=... cost=... USD`; ABSTAIN с кодом
  `invalid_output` — FAIL пробы.
- `marketdata-binance` нормализует символ общей `domain.CanonicalSymbol`
  (фьючерсный `BTC/USDT:USDT` идёт на спотовый рынок `BTCUSDT`).
- Разметка входит в бар, закрывшийся после вердикта гейта (а не после приёма
  запроса); если входной бар открылся слишком рано, вход сдвигается на
  следующий, а исход пишет `entryLagMs`/`entryShifted`.
- Повторный скан trend оценивает все новые закрытые бары и не переигрывает уже
  оценённые; пропущенные пересечения не теряются.
- Вердикт сохраняется при обрыве клиента; одновременные дубликаты `signalId`
  не дублируют вызов провайдера.
- Повтор `signalId` с другим телом — явный конфликт, а не чужой вердикт.
- Разметка требует подряд идущих баров и канонизирует `candlesHash`.
- Дедупликация решений и исходов перед агрегацией отчёта.
- `marketdata-binance`: нормализация символов, отказ на неизвестных символах,
  418 трактуется как неретраибельный бан IP.
- `decision-drex`: отклонение score-confidence вне диапазона.
- Журнал: append-only триггеры восстанавливаются при открытии, режимы файлов
  ужесточены.

### Этап E

- E1: источник, отклоняющий запрос (`invalid_request`), повторяется не дольше
  24 часов после закрытия окна разметки, затем пишется одно
  `outcome.unlabelable` с причиной `source_rejected` и сигнал больше не
  запрашивается. После 7-суточного срока любая ошибка источника повторяется не
  дольше 24 часов, затем пишется `timeout`.
- E2: локальный лимит Telegram (одно сообщение в минуту) больше не считается
  сбоем доставки: нет `notify.sent` с `ok:false`, счётчик сбоев не растёт,
  следующая попытка назначается на остаток задержки, в лог — только DEBUG.
- E3: человеческий отчёт и `--share` печатают `awaiting labeling`,
  `no outcome by deadline`, `source rejected`; в `domain.Report` и схему
  `report/v1` добавлено необязательное `sourceRejected`, существующие
  JSON-поля не менялись.
- E4: `lastLabelAt` восстанавливается из журнала при старте раннера.
- E5: `marketdata-binance` не запрашивает следующую страницу, если страница
  короче запрошенного лимита; постраничная выборка при полном лимите и большом
  `Limit` сохранена.
- E6: алерт `provider_auth`/`provider_misconfigured` отправляется, только если
  последний вердикт с этим кодом не старше 24 часов.
- E7: `tools/smokestub` — локальная заглушка decision/Binance/Telegram для
  репетиции runbook без сети; в релизные артефакты не входит.

### Этап F

- F1: окно `--share` считается по времени приёма `gate.request` (UTC), а не по
  `ts` из тела сигнала; добавлена строка `active days` — число разных UTC-дат с
  хотя бы одним запросом. Человеческий `gate report` печатает
  `window: days=N active=N`. Пустой журнал даёт `days: 0` и `active days: 0`.
- F2: длительность живой проверки приведена к 15 минутам в `README.md`,
  `docs/spec/03-core-cli.md` и `docs/release-checklist.md`; уточнены поля
  `status --json` и поведение статус-файла после остановки.
- F3: `docs/run-7-days.md` — запуск на неделю в терминале, отказ от служб,
  ежедневная проверка, ориентир 15 меток в сутки и отправка `--share` на 7-й
  день; ссылки из README, install и quickstart.
- F4: quickstart (EN/RU) исправлен по фактическому прохождению: шаблон запроса
  требует уникальный `signalId` и текущий `ts`, ответ показывает реальные
  `checks`, отмечена задержка первой метки при значениях по умолчанию.
- F5: задача `test:clients:python` в `Taskfile.yml` (`task check`) и команда в
  шаге 0 `docs/release-checklist.md`; без Python задача завершается понятным
  сообщением.
