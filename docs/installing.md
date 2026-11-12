# Installing, updating and removing opsie

## Install

opsie runs on Linux (amd64 and arm64).

```bash
curl -fsSL https://raw.githubusercontent.com/aloglu/opsie/main/install.sh | sh
```

The [script](../install.sh) downloads the prebuilt binary from the latest release, checks it against the release's checksums, and puts it in `~/.local/bin`. It doesn't need sudo or Go, and it tells you if that folder isn't on your PATH yet.

Then run `opsie`.

<details>
<summary>Installing with Go instead</summary>

```bash
go install github.com/aloglu/opsie/cmd/opsie@latest
```
</details>

## Updating

opsie checks for a new release once a day and shows it in the checklist. To install it:

```bash
opsie update
```

It shows what changed in every release since yours, installs the newest one, and swaps the binary in place. From inside the dashboard, open the update item and choose **Update opsie**; it restarts into the new version when it's done.

## Uninstall

```bash
opsie uninstall
```

It lists what it's about to remove and asks first: the login summary, the background checks service with its history (this part needs sudo), your settings, the audit log, and opsie itself. `--yes` skips the question, and `--keep-settings` leaves your settings and history for a later install.

Some things are yours and stay:

- **App backups** in `~/opsie-backups`, and the **key** that opens encrypted ones (`~/.config/opsie/backup-key.txt`). Without the key those backups can never be opened, so delete it only when you delete them.
- **Server settings you changed through opsie**, such as turning off SSH password logins or a sudo rule. They're the server's settings now; removing them would quietly undo your choices. The uninstall names the files.

If something can't be removed, opsie says why and keeps itself, so you can fix it and run the command again.
