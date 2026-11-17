# Recipe: TradingView → shadow gate

[Русская версия](tradingview.ru.md)

**In short: in M1 the gate does not accept TradingView webhooks directly.**
The reason is not laziness but transport incompatibility: TradingView alerts
cannot do what the local API requires. Below is why, what the options are, and
what must be decided before implementation.

## What TradingView can do

Platform limits (check the current TradingView documentation; they change):

- a webhook goes only to **port 80 or 443** over HTTP/HTTPS;
- about **3 seconds** are allowed for the answer, otherwise delivery is
  considered failed;
- **two-factor authentication** is mandatory on the account — without it,
  alerts with a webhook cannot be enabled;
- **arbitrary headers cannot be set**: the URL and the message body are
  configurable, but not `Authorization`. So `Authorization: Bearer <token>`
  cannot be sent;
- the alert body is free text with placeholder values; JSON has to be built by
  hand and is not typed.

## What the gate requires

`POST /v1/gate` from `vend serve`:

- listens on `127.0.0.1` only (loopback), default port `8420`;
- requires the `Authorization: Bearer <token>` header with the local token;
- requires `Content-Type: application/json` and a strict `GateRequest`:
  `signalId`, `instrument{venue,symbol}`, `side`, `intentKind`, `timeframe`,
  `ts`, `price`, `source`, `state`;
- rejects unknown fields and garbage in the body;
- is designed for a millisecond local call, not for the public internet.

There is no public endpoint, TLS termination or signature verification in M1 —
this is a deliberate local-first decision
([ADR-0005](../adr/0005-local-first.md)), not an omission.

## Why "just open the port" does not work

Even if `8420` is exposed to the internet, the result is:

1. **No authorization.** TradingView will not send a bearer token; there is
   nothing to check except the body, and anyone who learns the URL can send a
   body.
2. **No TLS and no proper client identity.** A secret in the alert URL ends up
   in logs and in the TradingView interface.
3. **No replay protection.** The same alert may arrive twice; the gate
   deduplicates by `signalId`, but `signalId` must be stable, and it still has
   to be parsed out of free text.
4. **3 seconds.** The gate may call a decision model; if it does not fit,
   TradingView retries and considers delivery broken.
5. **No mapping.** An alert is text, not a `GateRequest`: the ticker, side,
   timeframe and price need conversion with validation.

## Options for an ADR

1. **A public bridge service** (recommended if TradingView is needed): a
   separate process on a VPS with HTTPS on 443, its own secret in the alert
   body, signature/shared-secret verification, rate limiting, replay
   protection and a queue. The bridge answers TradingView `200` immediately
   and sends the `GateRequest` to the gate over a private channel (a tunnel to
   the owner's machine or a gate on the same host). This is a new attack
   surface and separate operations — only through an ADR.
2. **Manual/semi-automatic transfer**: the alert arrives by email or Telegram,
   and the owner or a separate script turns it into a `GateRequest` and calls
   the local API. Cheap, but not automation.
3. **Do not support TradingView in M1** (the current state). Signals come from
   Freqtrade and the built-in trend source; see the
   [Freqtrade recipe](freqtrade.md).

## What an ADR must contain before code

- the threat model of the public endpoint: authentication, replay, limits,
  logs without secrets;
- the mapping of an alert to a `GateRequest` (ticker, side, timeframe, price,
  `ts`);
- behavior on the 3-second timeout: a queue and an immediate `200`;
- where the bridge lives, who updates it, and how it reaches the decision
  journal;
- compatibility with `shadow`: the verdict is still only journaled.

Until such an ADR exists, there will be no recipe with code: accepting
TradingView webhooks is not claimed or tested in M1.
