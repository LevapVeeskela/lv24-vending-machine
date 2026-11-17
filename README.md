# LV24 Vending

**Платформа автоматической торговли с детерминированным ядром и сменными
ИИ-агентами.** Крипто-биржи и брокеры, LLM и decision-модели (Drex/Jev),
локальный терминал, веб и мобильный пульт.

> Статус: **релиз-кандидат 0.1.0-m1 (2026-11-16), не опубликован** —
> shadow-гейт с фоновой разметкой, trend-сигналы, Telegram-уведомления, арена
> моделей, `--share`, policy-фильтр, живые пробы `doctor --live`, канонические
> символы, оценка объёма сигналов `signals estimate` и локальная сборка релиза.
> Бинарники не подписаны; релиз собирается
> `go run ./tools/release` и никуда не загружается.
> Установка — [install](docs/install.md) / [install.ru](docs/install.ru.md);
> живая проверка на 15 минут — [runbook](docs/runbook-live-smoke.md);
> выпуск — [release checklist](docs/release-checklist.md).
> **НЕ сделано:** публикация релиза, адаптер TradingView webhook,
> desktop/web/mobile, paper/live, команды Telegram (двусторонний бот).
> Тесты — офлайн; живые вызовы — только ключом владельца.

## Идея

```
рынок ─▶ стратегии ─┐
                    ├─▶ proposals ─▶ RiskGate ─▶ OMS ─▶ биржа
ИИ-агенты ──────────┘                (код)      (код)
```

- **ИИ советует, код решает.** Агент возвращает типизированный `Proposal`;
  ордер может отправить только детерминированный слой после риск-проверок.
- **Провайдер — пакет, а не ветка `if`.** Биржа, брокер, данные, LLM,
  decision-модель — отдельные модули с дескриптором возможностей.
- **Фича вендора → свой агент.** Матрица «роль × вендор × класс активов»
  настраивается декларативно; несовместимые агенты не загружаются.
- **Объяснимо и воспроизводимо.** Журнал решений с hash-chain, реплей,
  калибровка моделей (Brier/ECE).
- **Local-first.** Один бинарник и SQLite на ноутбуке; тот же core — сервером
  с Postgres.

## Стек

| Слой | Технология |
|---|---|
| Core + CLI | Go 1.27 (`vend`; `vendd` — цель) |
| Desktop | Tauri 2 + React 19 + TypeScript (FSD) |
| Web | React 19 + TypeScript (общий `packages/ui-web`) |
| Mobile | Flutter + Riverpod |
| Контракты | OpenAPI 3.1 + JSON Schema → Go/TS/Dart |
| Хранение | SQLite (локально) / Postgres (сервер) |

## Документация

- [Установка (EN)](docs/install.md) · [Установка (RU)](docs/install.ru.md)
- [Быстрый старт (EN)](docs/quickstart.md) · [Быстрый старт (RU)](docs/quickstart.ru.md)
- [Живая проверка shadow-гейта](docs/runbook-live-smoke.md)
- [Семь дней работы и критерий G1](docs/run-7-days.md)
- [Чек-лист выпуска](docs/release-checklist.md)
- [Спецификация](docs/spec/README.md)
- [Ядро и CLI](docs/spec/03-core-cli.md)
- [Архитектура](docs/spec/01-architecture.md)
- [Провайдеры](docs/spec/04-providers.md)
- [Агенты и ИИ](docs/spec/05-agents.md)
- [Риск и исполнение](docs/spec/06-risk-execution.md)
- [План](docs/spec/10-roadmap.md)
- [Изменения](CHANGELOG.md)
- [ADR](docs/adr/)

## Рабочий quickstart (RC)

Установите `vend` ([install](docs/install.md) / [install.ru](docs/install.ru.md))
или соберите из корня репозитория. Офлайн, без ключа:

```bash
go build -o vend ./core/cmd/vend
vend init
vend gate test --request contracts/examples/gate-request.json \
  --fixture core/cmd/vend/testdata/decision-response.json
vend journal verify
vend gate report
```

Путь за 15 минут с ключом decision-провайдера, фоновой разметкой, `vend status`
и `gate report --share` — в [quickstart](docs/quickstart.md) /
[quickstart.ru](docs/quickstart.ru.md). Живые пробы (один вызов decision, две
свечи, тестовое сообщение Telegram при включении) — `vend doctor --live`;
`--save-raw <file>` пишет сырой ответ провайдера с правами 0600.
Длительный прогон для G1 (≥ 7 дней, ≥ 100 меток) — [семь дней работы](docs/run-7-days.md);
объём сигналов до старта проверяется `vend signals estimate --days 7`.

Тесты офлайн; живые вызовы провайдеров — только ключом владельца.

## Целевой UX (не реализовано)

```bash
vend init --template crypto-paper
vend provider connect exchange-binance --id binance-main
vend run start grid-btc --account binance-main --symbol BTC/USDT --mode paper
vend agent explain <decision-id>
vend serve            # desktop/web/mobile подключаются к API
```

## Безопасность

- Shadow-режим по умолчанию (ордера не отправляются); paper/live — отдельные
  этапы, live требует явного unlock с TTL и подтверждением.
- Ключи — только через `secretRef` и переменные окружения (в M1 — env;
  ОС-keychain — следующий шаг), не в конфиге и не в БД; приложение не видит их в UI.
- Журнал с hash-chain; kill switch, approve-очередь и reconciliation с биржей —
  обязательные условия для live (не реализованы).
- Никакой гарантии доходности: это инструмент, а не инвестиционная рекомендация.

## Лицензия

Ядро, провайдеры и SDK — **MIT** (см. `LICENSE`). Документация — CC-BY-4.0.
Мобильное приложение может распространяться как коммерческий продукт в App Store,
Google Play и RuStore — MIT это позволяет. AGPL/GPL-зависимости в ядре и мобильной
сборке запрещены: подробности в [ADR-0007](docs/adr/0007-license-and-stores.md).
