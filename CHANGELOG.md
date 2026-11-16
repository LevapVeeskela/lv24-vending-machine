# Changelog

Формат — [Keep a Changelog](https://keepachangelog.com/ru/1.1.0/),
версии — [SemVer](https://semver.org/lang/ru/). Релизы пока локальные:
артефакты собираются `go run ./tools/release`, публикации нет.

## [0.1.0-m1] — 2026-11-16

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
- Документация: `docs/quickstart.md` (EN), `docs/quickstart.ru.md` (RU),
  обновлённые `README.md`, `docs/spec/03-core-cli.md`, `docs/spec/README.md`,
  рецепты `docs/recipes/` и Python-клиент гейта.

### Changed

- Конфиг: `outcome.feeBps` по умолчанию 10 за сторону (20 round-trip),
  добавлены `outcome.labelIntervalSec`, `notify.telegram`, `signals.trend`,
  `arena`; валидация требует конкретных id моделей и ссылок на секреты.
- Гейт возвращает фактический `mode` из конфига и плоский wrapper
  `{mode, signalId, verdict}`.
- `journal verify`/`head` принимают общие `--config/--data-dir/--json`.
- Отчёт отделяет backfill-метки от обычных и печатает `insufficient data`
  до 100 размеченных исходов.

### Fixed

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
