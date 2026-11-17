# Установка `vend`

[English version](install.md)

Два пути до рабочего `vend`:

1. **Готовый бинарник** со страницы релизов GitHub (быстрее, когда релиз
   опубликован).
2. **Сборка из исходников** этого репозитория (работает уже сейчас, нужен Go).

> **Статус релиза.** В текущем состоянии репозитория релиз `0.1.0-m1` **не
> опубликован**. `go run ./tools/release` собирает `dist/` локально и ничего не
> загружает, поэтому скачиваемых файлов пока нет. Путь 1 описывает порядок
> действий после публикации предрелиза владельцем; до этого используйте путь 2.
> Бинарники не подписаны — прочитайте раздел
> [Неподписанные бинарники](#неподписанные-бинарники) перед запуском.
>
> **Что проверено.** Путь для Windows PowerShell ниже пройден целиком на файлах
> `0.1.0-m1` из `dist/`: контрольная сумма, `version`, `init`, офлайн-путь из
> quickstart и `doctor`. Отдельная проверка запустила `version`, `init` и
> `doctor` для `vend_linux_amd64` в WSL Ubuntu; остальной путь для Linux и все
> команды macOS по-прежнему **не запускались**, считайте их непроверенными,
> пока кто-то не повторит прохождение на этих системах.

## Путь 1 — готовый бинарник (после публикации)

### 1. Скачать

Откройте список релизов:

- <https://github.com/LevapVeeskela/lv24-vending-machine/releases> — список
  релизов; пре-релиз `0.1.0-m1` появится здесь после публикации владельцем.
- <https://github.com/LevapVeeskela/lv24-vending-machine/releases/latest> —
  появится только после стабильного выпуска и на пре-релиз не ведёт.

Скачайте файл для своей платформы и манифест контрольных сумм:

| Платформа | Файл |
|---|---|
| Windows x86-64 | `vend_windows_amd64.exe` |
| Linux x86-64 | `vend_linux_amd64` |
| Linux arm64 | `vend_linux_arm64` |
| macOS Intel | `vend_darwin_amd64` |
| macOS Apple silicon | `vend_darwin_arm64` |
| все | `SHA256SUMS` |

Это отдельные исполняемые файлы, не архивы. Положите их в один каталог, чтобы
проверка сумм нашла файлы.

### 2. Проверить контрольную сумму

`SHA256SUMS` — обычный манифест, по строке на файл:

```text
<sha256>  vend_linux_amd64
<sha256>  vend_windows_amd64.exe
```

Linux (GNU coreutils):

```bash
sha256sum -c SHA256SUMS --ignore-missing
# vend_linux_amd64: OK
```

Или проверьте один файл и сравните вывод со строкой манифеста:

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

Сравните со строкой для того же имени файла. **Несовпадение — файл запускать
нельзя**: скачайте заново и проверьте ещё раз.

### 3. Запустить

Linux / macOS:

```bash
chmod +x vend_linux_amd64
./vend_linux_amd64 version
mv vend_linux_amd64 vend
./vend version
# vend <version>
```

При желании установите в `PATH`:

```bash
sudo install -m 0755 vend /usr/local/bin/vend
```

Windows PowerShell:

```powershell
Rename-Item .\vend_windows_amd64.exe vend.exe
.\vend.exe version
# vend <version>
```

Затем создайте конфиг и журнал:

```bash
./vend init
./vend doctor
```

Windows PowerShell:

```powershell
.\vend.exe init
.\vend.exe doctor
```

`init` печатает пути конфига и данных, `doctor` проверяет их без сети. Сначала
запускайте `init`, потом `doctor`: без конфига `doctor` не проходит проверки
конфига, БД, журнала, провайдеров и policy и возвращает 1. Дальше —
[quickstart.ru.md](quickstart.ru.md) или живая проверка на 15 минут в
[runbook-live-smoke.ru.md](runbook-live-smoke.ru.md). Офлайн-путь B в quickstart
начинается с того же `init`; пропустите эту строку, если только что выполнили
`init` здесь — повторный запуск завершается кодом 4 с `config already exists`.
Длительный прогон для G1 (≥ 7 дней, ≥ 100 меток) —
[run-7-days.ru.md](run-7-days.ru.md).

## Неподписанные бинарники

Бинарники `0.1.0-m1` **не подписаны и не нотаризованы**. Единственная проверка
целостности — манифест SHA-256; предупреждение платформы ожидаемо и само по себе
не доказывает подмену. Не обходите предупреждение для файла, чью контрольную
сумму вы не проверили.

### macOS Gatekeeper

У скачанного бинарника появляется атрибут `com.apple.quarantine`:

```bash
xattr -l ./vend
# com.apple.quarantine: ...
```

Варианты, по убыванию предпочтительности:

1. В Finder нажмите на `vend` правой кнопкой (или Control+клик) → **Открыть** →
   **Открыть** в диалоге. macOS запомнит выбор для этого файла.
2. Если в диалоге есть только **Отмена**, откройте **Системные настройки →
   Конфиденциальность и безопасность**, найдите сообщение о блокировке `vend`
   и нажмите **Всё равно открыть**.
3. Уберите атрибут явно после проверки суммы:

   ```bash
   xattr -d com.apple.quarantine ./vend
   ```

Gatekeeper не заменяет проверку контрольной суммы; сделайте и то, и другое.

### Windows SmartScreen

При первом запуске может появиться **«Windows защитила ваш компьютер»**:

1. Нажмите **Подробнее**.
2. Нажмите **Выполнить в любом случае**.

Если файл скачан браузером, у него может быть метка Mark-of-the-Web; после
проверки хэша её можно снять:

```powershell
Unblock-File .\vend.exe
```

Подписи Authenticode пока нет, поэтому SmartScreen будет предупреждать до
подписания бинарника в одном из следующих релизов.

### Linux

Аналога Gatekeeper нет. Нужен `chmod +x`; некоторые файловые менеджеры требуют
включить «Разрешить выполнение файла как программы» в свойствах.

## Путь 2 — сборка из исходников

Требования: Go 1.27 (см. `go.work`) и клон этого репозитория.

```bash
git clone https://github.com/LevapVeeskela/lv24-vending-machine.git
cd lv24-vending-machine
go build -o vend ./core/cmd/vend
./vend version
# vend <version>
```

На Windows собирайте с `-o vend.exe` и запускайте `.\vend.exe`.

Замечания:

- Сборка запускается из корня репозитория: `go.work` объединяет модули core,
  провайдеров и tools.
- Сборка скачивает только Go-модули; ничего не публикует и не требует
  релизного инструмента.
- Версия сборки из исходников — значение `version` из
  `core/cmd/vend/main.go`, в этом дереве `0.1.0-m1`. `tools/release`
  подставляет то же значение через `-ldflags "-X main.version=<version>"`.

## Проверка установки

```bash
./vend version          # печатает версию
./vend init             # создаёт конфиг и журнал при первом запуске
./vend doctor           # офлайн: конфиг, каталог данных, БД, журнал, провайдеры, наличие секрета, policy hash
```

`doctor` возвращает 0, если все офлайн-проверки прошли, и 1 при любой ошибке.
Сетевые пробы включаются флагом `vend doctor --live`; что означает каждый код —
в [runbook](runbook-live-smoke.ru.md).
