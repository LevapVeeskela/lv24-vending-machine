# Contributing to LV24 Vending

Contributions are accepted under **Apache-2.0**: by submitting a change you
agree that it is licensed under the same terms as the project (section 5 of the
[LICENSE](LICENSE)). There is no CLA to sign.

Keep commits small and use Conventional Commits. A pull request should state the
risk and what was checked.

## Verify a change

Run the commands from step 0 of the
[release checklist](docs/release-checklist.md), from the repository root.
`task` may not be installed; these are the same checks without it:

```bash
go -C core test ./contracts -run TestGeneratedContractsMatch   # gen:check
go run ./tools/lintimports                                     # lint:imports
go -C core test ./...
go -C providers/decision-drex test ./...
go -C providers/marketdata-binance test ./...
go -C providers/notify-telegram test ./...
go -C tools test ./...
VEND_E2E=1 go -C tools test -count=1 ./e2e/...                 # end-to-end rehearsal
python -m unittest discover -s examples -p "test_*.py"
go build all
```

The end-to-end test builds `vend` and `tools/smokestub` into a temporary
directory, starts both on free loopback ports and stops them by exact PID. In
PowerShell set the variable first:
`$env:VEND_E2E='1'; go -C tools test -count=1 ./e2e/...`. Without `VEND_E2E=1`
the package is skipped, so an ordinary `go test ./...` stays fast. If `python`
is missing but `python3` exists, use `python3` for the client and example tests.

Set `VEND_E2E_BIN` to a prebuilt `vend` (absolute, or relative to the repository
root) to run that file instead of building one; the release checklist uses it on
the `dist/` artifact before tagging.

When a rehearsal step fails, the test keeps its evidence in a directory under
the system temp (`vend-e2e-failure-*`) and prints the path: the `vend serve`
stdout and stderr, the stub `/ctl/stats` answer, `vend status --json`, the last
200 `vend journal tail --json` events and the config. Every file is passed
through a redaction filter, so the generated provider tokens never reach it. A
passing run removes the directory.

## Hard rules

- **No secrets** in code, config, the journal, logs, tests or commits. Secrets
  are referenced as `conn/<id>/<field>` and are read from `VENDING_SECRET_*`
  environment variables; OS keychain support is planned and not implemented.
- **Money is never `float64`.** Use `Decimal`; at boundaries pass strings.
- **No AGPL/GPL code** may be copied or linked; see
  [ADR-0007](docs/adr/0007-license-and-stores.md).
- **No live exchange calls in tests or CI.** Only sandbox, paper or mock
  providers; tests never use real keys.
- **Do not edit generated files** (`*.gen.go`, `schema.d.ts`, Dart generated).
  Change the schema in `contracts/` and regenerate.
- Never bypass `RiskGate`: any path to an exchange goes
  `OrderIntent → Evaluate → Reserve → OMS`.

## Security

Do not open a public issue for a vulnerability. Report it privately as
described in [SECURITY.md](SECURITY.md).
