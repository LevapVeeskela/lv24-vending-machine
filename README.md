# LV24 Vending

**Платформа автоматической торговли с детерминированным ядром и сменными
ИИ-агентами.** Крипто-биржи и брокеры, LLM и decision-модели (Drex/Jev),
локальный терминал, веб и мобильный пульт.

> Статус: **спецификация v0.1**. Код ещё не начат — см. `docs/spec/10-roadmap.md`.

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
| Core + CLI | Go 1.27 (`vend`, `vendd`) |
| Desktop | Tauri 2 + React 19 + TypeScript (FSD) |
| Web | React 19 + TypeScript (общий `packages/ui-web`) |
| Mobile | Flutter + Riverpod |
| Контракты | OpenAPI 3.1 + JSON Schema → Go/TS/Dart |
| Хранение | SQLite (локально) / Postgres (сервер) |

## Документация

- [Спецификация](docs/spec/README.md)
- [Архитектура](docs/spec/01-architecture.md)
- [Провайдеры](docs/spec/04-providers.md)
- [Агенты и ИИ](docs/spec/05-agents.md)
- [Риск и исполнение](docs/spec/06-risk-execution.md)
- [План](docs/spec/10-roadmap.md)
- [ADR](docs/adr/)

## Целевой UX

```bash
vend init --template crypto-paper
vend provider connect exchange-binance --id binance-main
vend run start grid-btc --account binance-main --symbol BTC/USDT --mode paper
vend agent explain <decision-id>
vend serve            # desktop / web / mobile подключаются к API
```

## Безопасность

- Paper-режим по умолчанию; live — явный unlock с TTL и подтверждением.
- Ключи — в ОС-keychain, не в конфиге и не в БД; приложение не видит их в UI.
- Kill switch, approve-очередь, reconciliation с биржей, аудит каждого решения.
- Никакой гарантии доходности: это инструмент, а не инвестиционная рекомендация.

## Лицензия

TBD (предложение: MIT для ядра и паков, CC-BY-4.0 для документации).
