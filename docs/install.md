# Install `vend`

[Русская версия](install.ru.md)

Two paths to a working `vend`:

1. **Prebuilt binary** from the GitHub release page (fastest, once the release
   is published).
2. **Source build** from this repository (works today, needs Go).

> **Release status.** In this repository state the `0.1.0-m1` release is **not
> published**. `go run ./tools/release` builds `dist/` locally and uploads
> nothing, so there is no downloadable asset yet. Path 1 describes the flow for
> when the owner publishes the pre-release; until then use path 2. The binaries
> are unsigned — read [Unsigned binaries](#unsigned-binaries) before running
> one.
>
> **What was verified.** The Windows PowerShell path below was walked end to
> end on the `0.1.0-m1` files from `dist/`: checksum, `version`, `init`, the
> offline path from quickstart and `doctor`. A separate check ran `version`,
> `init` and `doctor` on `vend_linux_amd64` under WSL Ubuntu; the rest of the
> Linux path and all macOS commands were **not** run, so treat them as
> unverified until someone repeats the walkthrough on those systems.

## Path 1 — prebuilt binary (after publication)

### 1. Download

Open the releases list:

- <https://github.com/LevapVeeskela/lv24-vending-machine/releases> — the
  releases list; the `0.1.0-m1` pre-release appears here after the owner
  publishes it.
- <https://github.com/LevapVeeskela/lv24-vending-machine/releases/latest> —
  appears only after a stable release and does not point at a pre-release.

Download the asset for your platform and the checksum manifest:

| Platform | Asset |
|---|---|
| Windows x86-64 | `vend_windows_amd64.exe` |
| Linux x86-64 | `vend_linux_amd64` |
| Linux arm64 | `vend_linux_arm64` |
| macOS Intel | `vend_darwin_amd64` |
| macOS Apple silicon | `vend_darwin_arm64` |
| all | `SHA256SUMS` |

The assets are plain executables, not archives. Put them in one directory so
the checksum check can find them.

### 2. Verify the checksum

`SHA256SUMS` is a plain manifest, one line per asset:

```text
<sha256>  vend_linux_amd64
<sha256>  vend_windows_amd64.exe
```

Linux (GNU coreutils):

```bash
sha256sum -c SHA256SUMS --ignore-missing
# vend_linux_amd64: OK
```

or check one file and compare the printed hash with its manifest line:

```bash
sha256sum vend_linux_amd64
# <sha256>  vend_linux_amd64
```

macOS:

```bash
shasum -a 256 vend_darwin_arm64
# <sha256>  vend_darwin_arm64
```

Windows PowerShell:

```powershell
(Get-FileHash .\vend_windows_amd64.exe -Algorithm SHA256).Hash.ToLower()
# <sha256>
Get-Content .\SHA256SUMS
```

Compare against the line for the same file name. **A mismatch means do not run
the file**: download it again and re-check.

### 3. Run

Linux / macOS:

```bash
chmod +x vend_linux_amd64
./vend_linux_amd64 version
mv vend_linux_amd64 vend
./vend version
# vend <version>
```

Optionally install it on `PATH`:

```bash
sudo install -m 0755 vend /usr/local/bin/vend
```

Windows PowerShell:

```powershell
Rename-Item .\vend_windows_amd64.exe vend.exe
.\vend.exe version
# vend <version>
```

Then create the config and journal. The G1 profile enables the trend source,
so a fresh install is not silent:

```bash
./vend init --quick
./vend doctor --strict
```

Plain `./vend init` writes the same file with the trend source disabled and no
symbols; use it only when your own bot sends the signals. Windows PowerShell:
`.\vend.exe init --quick` and `.\vend.exe doctor --strict`.

`init` prints the config and data paths plus the export commands; `doctor
--strict` checks the offline G1 invariants (trend, ten symbols, 15m, secrets)
but never claims the signal volume is verified — run `vend signals estimate
--days 7` for that. Continue with
[quickstart](quickstart.md) (or the 15 minute live check in
[runbook-live-smoke.md](runbook-live-smoke.md)). The full walkthrough in
quickstart starts with its own `init`; skip that line when you have just run
`init` here — a second run exits 4 with `config already exists`. For the
multi-day G1 run (≥ 7 days, ≥ 100 labels) see [run-7-days.md](run-7-days.md).

## Unsigned binaries

The `0.1.0-m1` binaries are **not code-signed and not notarized**. The SHA-256
manifest is the only integrity check; a platform warning is expected and is not
by itself proof of tampering. Do not bypass a warning on a file whose checksum
you have not verified.

### macOS Gatekeeper

A downloaded binary gets the `com.apple.quarantine` attribute:

```bash
xattr -l ./vend
# com.apple.quarantine: ...
```

Options, in order of preference:

1. In Finder, right-click (or Control-click) `vend` → **Open** → **Open** in the
   dialog. macOS remembers the choice for that file.
2. If the dialog only offers **Cancel**, open **System Settings → Privacy &
   Security**, find the blocked `vend` message and click **Open Anyway**.
3. Remove the attribute explicitly after verifying the checksum:

   ```bash
   xattr -d com.apple.quarantine ./vend
   ```

Gatekeeper does not replace the checksum check; do both.

### Windows SmartScreen

The first run may show **"Windows protected your PC"**:

1. Click **More info**.
2. Click **Run anyway**.

If the file came from a browser download it may also carry the Mark-of-the-Web;
after verifying the hash you can clear it:

```powershell
Unblock-File .\vend.exe
```

There is no Authenticode signature yet, so SmartScreen will keep warning until
the binary is signed in a later release.

### Linux

There is no Gatekeeper equivalent. `chmod +x` is required; some file managers
also need "Allow executing file as program" in the file properties.

## Path 2 — build from source

Requirements: Go 1.27 (see `go.work`) and a checkout of this repository.

```bash
git clone https://github.com/LevapVeeskela/lv24-vending-machine.git
cd lv24-vending-machine
go build -o vend ./core/cmd/vend
./vend version
# vend <version>
```

On Windows build with `-o vend.exe` and run `.\vend.exe`.

Notes:

- Run the build from the repository root: `go.work` joins the core, provider
  and tools modules.
- The build downloads Go modules only; it does not publish anything and does not
  need the release tool.
- A source build prints the `version` value from `core/cmd/vend/main.go`, which
  is `0.1.0-m1` in this tree. `tools/release` stamps the same value with
  `-ldflags "-X main.version=<version>"`.

## Verify the install

```bash
./vend version          # prints the version
./vend init --quick     # writes the G1 profile config + journal on first use
./vend doctor --strict  # offline: config, data dir, database, journal, providers, secrets, policy hash, G1 profile
```

`doctor --strict` exits 0 when every offline check passes and 1 when one fails.
It does not verify the signal volume; `vend signals estimate --days 7` does.
Network probes are opt-in with `vend doctor --live`; see the
[runbook](runbook-live-smoke.md) for what each live code means.
