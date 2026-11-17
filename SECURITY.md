# Security Policy

[Русская версия](SECURITY.ru.md)

## What counts as a vulnerability

- bypassing `RiskGate`, executing an order without checks, raising limits without approval;
- leaking API keys, tokens, private keys or personal data;
- bypassing live-unlock, the kill switch or the approval queue;
- tampering with market data/fills in a way that affects decisions;
- prompt injection that leads to an action with money;
- remote code execution through a provider pack, a WASM strategy or the API;
- substitution of release updates/artifacts.

## How to report

Privately, through a GitHub Security Advisory in this repository:
<https://github.com/LevapVeeskela/lv24-vending-machine/security/advisories/new>.
Do not open a public issue before the fix.

If you cannot use GitHub, write to the support mailbox `lv24by.help@mail.ru`
with the subject `Security: lv24-vending-machine` and ask for a private
channel. It is a shared support mailbox, so do not put the vulnerability
details or any secrets in that first message. All project contacts are listed
at <https://lv24.by/contacts>.

In the report: version/commit, configuration (paper/live), reproduction steps,
impact, logs without secrets.

## What we do

- Confirm receipt within 72 hours.
- Prepare a fix and a release; coordinate disclosure.
- Credit the reporter in the release notes (unless anonymity was requested).

## Scope

In scope: the code in this repository, release artifacts, contracts, provider
packs, the WASM runtime, the desktop shell, the mobile app.

Out of scope: third-party exchanges/brokers, their APIs and policies;
LLM/decision-provider models; the user's infrastructure.

## Rules for contributors

- No secrets in code, tests, fixtures or git history.
- Money — `Decimal` only, no `float`.
- Any path to an exchange — only through `OrderIntent → RiskGate → OMS`.
- AGPL/GPL dependencies in the core and mobile are forbidden (see
  `docs/adr/0007`).
- Tests do not call real exchanges and do not use real keys.
