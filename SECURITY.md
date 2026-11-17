# Security Policy

[Русская версия](SECURITY.ru.md)

## Scope: the M1 shadow gate

M1 is a local shadow gate. It evaluates a signal, asks a decision provider for a
second opinion and writes the verdict to a hash-chained SQLite journal. It has
**no order execution path, no OMS, no live trading, no kill switch, no approval
queue, no WASM runtime and no desktop or mobile client**. A report about those
features does not describe this release.

## What counts as a vulnerability

- bypassing authentication on `POST /v1/gate`, or exposing the API beyond
  loopback;
- leaking API keys, tokens or secrets through the config, the journal, the logs,
  a report, `--save-raw`, an error message or crash output;
- tampering with the journal or its hash chain, or making `journal verify` or
  `gate report` accept a broken chain;
- manipulating the market-data or decision-provider responses used by the gate,
  including prompt injection that changes a verdict;
- bypassing request validation or the `signalId` idempotency rules so that a
  replayed or conflicting signal gets a verdict that does not match the journal;
- substitution of release updates or artifacts.

## How to report

Privately, through a GitHub Security Advisory in this repository:
<https://github.com/LevapVeeskela/lv24-vending-machine/security/advisories/new>.
Do not open a public issue before the fix.

If you cannot use GitHub, write to the support mailbox `lv24by.help@mail.ru`
with the subject `Security: lv24-vending-machine` and ask for a private
channel. It is a shared support mailbox, so do not put the vulnerability
details or any secrets in that first message. All project contacts are listed
at <https://lv24.by/contacts>.

In the report: version/commit, configuration (mode and providers), reproduction
steps, impact, logs without secrets.

## What we do

- Confirm receipt within 72 hours.
- Prepare a fix and a release; coordinate disclosure.
- Credit the reporter in the release notes (unless anonymity was requested).

## Scope

In scope: the Go core and CLI (`vend`), the loopback HTTP API, the SQLite
journal, the provider adapters (`decision-drex`, `marketdata-binance`,
`notify-telegram`), the contracts and the release artifacts.

Out of scope: execution, OMS, `RiskGate`, kill switch and the approval queue
(not implemented in M1); desktop, web and mobile clients (not part of M1);
third-party exchanges and brokers, their APIs and policies; LLM/decision-provider
models; the user's infrastructure.

## Rules for contributors

- No secrets in code, tests, fixtures or git history.
- Money — `Decimal` only, no `float`.
- M1 has no path to an exchange. Any future execution must go through
  `OrderIntent → Evaluate → Reserve → OMS`; that path does not exist yet.
- AGPL/GPL dependencies in the core and mobile are forbidden (see
  `docs/adr/0007`).
- Tests do not call real exchanges and do not use real keys.
