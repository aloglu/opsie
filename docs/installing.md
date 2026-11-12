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

If you set up the login summary, run `opsie motd --remove`. If you turned on background checks, turn them off from the dashboard first (or `sudo systemctl disable --now opsie && sudo rm /etc/systemd/system/opsie.service && sudo rm -r /usr/local/libexec/opsie /var/lib/opsie`). Then:

```bash
rm ~/.local/bin/opsie
rm -rf ~/.config/opsie ~/.local/state/opsie
```
