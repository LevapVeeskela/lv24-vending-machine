# LV24 Vending

**Платформа автоматической торговли с детерминированным ядром и сменными
ИИ-агентами.** Крипто-биржи и брокеры, LLM и decision-модели (Drex/Jev),
локальный терминал, веб и мобильный пульт.

> Статус: **M0 (wedge core) готов, M1 часть 1 готова 2026-11-16** (локальный
> shadow-гейт, разметка исходов, отчёт только по журналу). НЕ сделано: Telegram,
> источник trend-сигналов, арена моделей, рецепты Freqtrade/TradingView, релизные
> бинарники, `--share`. Тесты — офлайн; живые вызовы — только ключом владельца.

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

## Рабочий quickstart (реализовано в M1)

```bash
go build -o vend ./core/cmd/vend   # из корня workspace
vend init
vend gate test --request contracts/examples/gate-request.json \
  --fixture core/cmd/vend/testdata/decision-response.json
vend journal verify
VENDING_SECRET_CONN_LOCAL_APITOKEN=<token> vend serve   # 127.0.0.1:8420
curl -X POST http://127.0.0.1:8420/v1/gate \
  -H "Authorization: Bearer <token>" -H "Content-Type: application/json" \
  -d @contracts/examples/gate-request.json
vend outcomes label --candles <file>
vend gate report
vend journal verify
```

Тесты офлайн; живые вызовы провайдеров — только ключом владельца.

## Целевой UX (не реализовано в M1)

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

Ядро, провайдеры и SDK — **MIT** (см. `LICENSE`). Документация — CC-BY-4.0.
Мобильное приложение может распространяться как коммерческий продукт в App Store,
Google Play и RuStore — MIT это позволяет. AGPL/GPL-зависимости в ядре и мобильной
сборке запрещены: подробности в [ADR-0007](docs/adr/0007-license-and-stores.md).
