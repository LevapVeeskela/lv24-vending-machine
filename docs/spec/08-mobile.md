# 08. Mobile (Flutter)

Статус: draft v0.1 · Зависит от: `01-architecture.md`, `07-clients-desktop-web.md`

## 1. Роль mobile

Mobile — не «уменьшенный терминал», а **пульт и очередь подтверждений**:

- посмотреть портфель, P&L, экспозицию, состояние ботов;
- увидеть, что «требует внимания» (расхождение, простой провайдера, риск-алерт);
- подтвердить/отклонить сделку из approve-очереди;
- включить kill switch / экстренно закрыть позиции;
- получать push по критичным событиям;
- при необходимости — открыть график и историю решений.

Тяжёлая настройка (агенты, модели, бэктесты, политики) остаётся в desktop/web.
Mobile может менять базовые вещи (пауза run, лимиты из шаблонов).

## 2. Архитектура

```
apps/mobile/
├── app/                        # приложение (роутинг, тема, локализация)
├── packages/
│   ├── vending_api/            # сгенерированный клиент (из OpenAPI) + DTO
│   ├── vending_auth/           # pairing, токены, биометрия, сессии
│   ├── vending_realtime/       # SSE + реконнект + resume + батчинг
│   ├── vending_models/         # доменные модели и маппинг (freezed)
│   ├── vending_ui/             # дизайн-система, виджеты, графики
│   └── vending_push/           # FCM/APNs, маршрутизация уведомлений
├── melos.yaml
└── pubspec.yaml
```

Слои внутри `app`: `features/<name>/{data,domain,presentation}`; зависимости —
через Riverpod-провайдеры; прямой доступ к `dio` из UI запрещён.

Стек (по итогам `ideas/flutter-mobile-trading`):

| Задача | Выбор | Версия | Причина |
|---|---|---|---|
| State | Riverpod | 3.5 | тестируемость, compile-time safety, без BuildContext |
| HTTP | `dio` + `openapi_generator` (`dart-dio`) | 5.11 / 7.0 | contract-first клиент из OpenAPI |
| Модели | `freezed` + `json_serializable` | 4.0 / 6.15 | неизменяемость, union-типы |
| Кэш/оффлайн | `drift` (+sqlcipher опц.) | 2.35 | реактивные запросы, миграции |
| Секреты | `flutter_secure_storage` | 11.2 | Keychain/Keystore, biometric-опции |
| Биометрия | `local_auth` | 3.0 | нужен `FlutterFragmentActivity` |
| Realtime | свой SSE-пакет (`Last-Event-ID`, backoff, ref-count) | — | контроль resume и подписок |
| Графики | `fl_chart` (`CandlestickChart`) | 1.3.1 | уже есть свечи/tooltip/painter |
| Push | `firebase_messaging` + `flutter_local_notifications` | 17.0 / 23.0 | id + deep link, данные из API |
| Навигация | `go_router` | 18.1 | deep links из push |
| Числа | `decimal` | 3.2 | без потери точности |
| Качество | `very_good_analysis`, `mocktail`, `patrol` | 11.0 / 1.0 / 4.11 | линт, тесты, e2e |
| Локализация | ARB + `flutter_localizations` | — | ru/en с первого дня |

Паттерны, которые берём (не код):
- `ChartViewport extends ChangeNotifier` передаётся в painter как `repaint:` —
  пан/зум не вызывает `setState`, рисуется только видимый `sublist`.
- Пул стримов на инструмент с ref-count и отложенным закрытием, probe при
  возврате приложения из фона, single-flight refresh на 401.
- `UiState.valueOrFallback` — stale-данные показываются с меткой, а не пустым
  экраном.
- Лицензии: GPL/AGPL/без-лицензии репо — только чтение паттернов; код берём
  лишь из MIT/BSD/Apache.

## 3. Подключение к ядру

Два режима:

1. **Через сервер** (`vendd` с Postgres, публичный HTTPS) — основной.
2. **Через desktop по LAN** — pairing QR: desktop показывает одноразовый код,
   mobile получает device-token (scoped, TTL, отзываемый), связь по HTTPS с
   самоподписанным сертификатом, закреплённым при pairing (SPKI pin).

Pairing:
```
desktop: vend device pair --ttl 5m   → QR {url, code, spkiPin}
mobile:  scan → POST /api/v1/devices/pair {code, deviceName, pubkey}
desktop: approve (в UI) → device-token + refresh-token
mobile:  secure storage; далее — обычные запросы с device-token
```

Токены: короткоживущий access (15 мин) + refresh (30 дней), отзыв per-device,
список устройств в desktop/web, «выйти на всех».

## 4. Экраны v1

| Экран | Содержимое |
|---|---|
| **Обзор** | equity, P&L, экспозиция, режим, «требует внимания», статус провайдеров |
| **Портфель** | позиции по счетам, распределение, дневной P&L, быстрый close |
| **Боты** | список run: статус, P&L, последний сигнал; пауза/возобновление |
| **Решения** | лента: proposal, кто, уверенность, вердикт риска, исход; trace |
| **Подтверждения** | очередь approve с TTL и таймером; accept/reject с заметкой |
| **Алерты** | критичные события, фильтры, отметка «прочитано» |
| **Рынок** | watchlist, свечи, индикаторы (лёгкая версия) |
| **Настройки** | устройства, уведомления, тема, язык, биометрия, выход |

Kill switch — на «Обзоре» и в AppBar экрана «Боты», с подтверждением и
биометрией. Approve — только после биометрии (настраивается).

## 5. Оффлайн и синхронизация

- Последний снапшот портфеля/ботов хранится в `drift`, показывается с меткой
  «данные на HH:MM».
- Мутации (approve, pause, kill) не буферизуются: без сети — явная ошибка, не
  «тихая очередь». Деньги не любят отложенных команд.
- Realtime resume через `Last-Event-ID`; при недоступности — polling каждые 30 с
  с индикатором.
- Push: критичные (kill, reconcile, approve, drawdown) — всегда; обычные —
  по настройке; payload не содержит сумм и ключей, только id + тип.

## 6. Безопасность

- Секреты и токены — только secure storage; логи/messages без токенов.
- Jailbreak/root — предупреждение, не блокировка (продукт для владельца).
- Скриншоты чувствительных экранов — `FLAG_SECURE` (Android) / blur (iOS) опция.
- Биометрия для approve/kill/изменения настроек; PIN-фолбэк.
- Сертификат-пиннинг для LAN-режима; для сервера — обычный TLS + HSTS.
- Минимум permissions: сеть, уведомления, камера (QR), биометрия. Гео/контакты
  не запрашиваем.

## 7. Качество

- `dart analyze --fatal-infos`, `dart format --set-exit-if-changed`, `flutter test`.
- Widget-тесты на критичные потоки: approve, kill, оффлайн-баннер.
- Integration-тест (patrol/integration_test) на pairing + paper-ордер.
- Размер: APK arm64 < 25 МБ, старт < 2 с на среднем устройстве.
- Доступность: динамический шрифт, контраст, семантика, RTL-готовность (не в v1).

## 8. Что не делаем в mobile v1

- Полный редактор агентов/промптов.
- Запуск бэктестов и оптимизаций.
- Управление ключами бирж (только через desktop/web).
- Маржинальные/фьючерсные сложные ордера (только просмотр и close).
