# LV24 Vending — спецификация

Рабочее имя продукта: **LV24 Vending** · репозиторий: `lv24-vending-machine`
CLI: `vend` · демон: `vendd` · стек: Go 1.27 · Tauri 2 + React 19 · Flutter 3.47

## Документы

| # | Документ | О чём |
|---|---|---|
| 00 | [vision](00-vision.md) | продукт, роли, scope, non-goals, принципы, термины |
| 01 | [architecture](01-architecture.md) | слои, порты, реестр, runtime, контракты, безопасность |
| 02 | [monorepo](02-monorepo.md) | дерево репо, тулинг, кодоген, CI, конвенции |
| 03 | [core-cli](03-core-cli.md) | команды CLI, форматы, коды возврата, WASM-стратегии |
| 04 | [providers](04-providers.md) | контракты портов, capability-модель, деградации, conformance |
| 05 | [agents](05-agents.md) | роли, манифесты, рантайм, калибровка, evals, guardrails |
| 06 | [risk-execution](06-risk-execution.md) | OrderIntent, RiskGate, reservation, reconciliation, аудит |
| 07 | [desktop-web](07-clients-desktop-web.md) | Tauri-шелл, экраны, realtime, web-режим |
| 08 | [mobile](08-mobile.md) | Flutter, pairing, approve, push, оффлайн |
| 09 | [research](09-research.md) | карта open-source решений и что заимствуем |
| 10 | [roadmap](10-roadmap.md) | этапы, критерии приёмки, риски |

ADR: [0001 Go](../adr/0001-go-core.md) · [0002 contract-first](../adr/0002-contract-first.md) ·
[0003 deterministic core](../adr/0003-deterministic-core.md) ·
[0004 provider registry](../adr/0004-provider-registry.md) ·
[0005 local-first](../adr/0005-local-first.md) ·
[0006 decision models](../adr/0006-decision-models.md) ·
[0007 license & stores](../adr/0007-license-and-stores.md)

По итогам ревью: [0008 wedge-first scope](../adr/0008-wedge-first-scope.md) (accepted) ·
[0009 два режима оплаты ИИ](../adr/0009-byok-and-what-we-sell.md) (accepted) ·
[0010 ограничения decision-моделей](../adr/0010-decision-model-guardrails.md) (accepted) ·
[0011 география и издатель](../adr/0011-geography-and-publisher.md) (accepted)

Ревью: [вердикт](../review/00-verdict.md) · [находки](../review/01-findings.md) ·
[фичи](../review/02-features.md) · [план v0.2](../review/03-plan-v2.md)

## Ключевые решения (кратко)

1. **Ядро на Go**, CLI + локальный демон, один контракт для трёх клиентов.
2. **ИИ совещательный, код решает**: `Proposal → RiskGate → Reservation → OMS`.
3. **Провайдер = пакет с дескриптором возможностей**, а не ветка в ядре.
4. **Capability-модель**: вендорские фичи объявляются, деградации — явные.
5. **Decision-модели (Drex/Jev) — отдельный порт**, не «LLM с JSON».
6. **Local-first**: SQLite + sidecar в desktop; Postgres + сервер — тот же код.
7. **Contract-first**: OpenAPI + JSON Schema, кодоген в Go/TS/Dart, drift-тест.
8. **Paper по умолчанию**, live — явный unlock с TTL и подтверждением.
9. **Журнал с hash-chain**: каждое решение объяснимо и воспроизводимо.
10. **Расширяемость**: стратегии — WASM, агенты — YAML-паки, провайдеры — Go-модули.

## Быстрый старт (целевой UX)

```bash
vend init --template crypto-paper
vend provider connect exchange-binance --id binance-main
vend run start grid-btc --account binance-main --symbol BTC/USDT --mode paper
vend run inspect <run-id>
vend agent explain <decision-id>
vend serve            # desktop/web/mobile подключаются к этому API
```

## Открытые вопросы к владельцу

| # | Вопрос | Решение |
|---|---|---|
| 1 | Название и CLI | `vend` + `vendd`, репо `lv24-vending-machine` (рабочее) |
| 2 | Лицензия | **MIT** для ядра/провайдеров/SDK, docs CC-BY-4.0; mobile можно продавать (ADR-0007) |
| 3 | Первые рынки | **Binance spot** (M1), затем Bybit и Alpaca |
| 4 | Live в v1 | paper-only до M3, live — после чек-листа и 2 недель paper |
| 5 | Push | FCM/APNs для v1, self-hosted (ntfy/UnifiedPush) как опция |
| 6 | Multi-user сервер | архитектурно готов, продуктом — после v1 |
| 7 | Публичный репозиторий | **да** (бесплатный CI), с `SECURITY.md` и без секретов в истории |

## Статус

- Ресёрч: 13 направлений, клоны и разборы в `ideas/` (вне репозитория; `ideas/INDEX.md`).
- Спека: draft v0.1; `00-vision.md` и `10-roadmap.md` — v0.2 по итогам скептического
  ревью, приняты владельцем 2026-11-16 (ветка `review/skeptical-v1`). Документы 01–09
  описывают целевую платформу; состав v1 сужен ADR-0008.
- Wire-пример в `04-providers.md §10` исправлен по документации вендора (ADR-0010).
- `domain` и `ports` — публичные пакеты `core/domain` и `core/ports`, а не
  `core/internal/…`: иначе модули провайдеров не могут их импортировать
  (`02-monorepo.md §2`).
- Код: M0 начат в ветке `feat/m0-wedge-core`; задание — `docs/plans/m0-wedge-core.md`.
