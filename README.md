# opsie

A dashboard for your server that lives in the terminal. opsie keeps an eye on CPU, memory, disks, network, containers, services and security settings, checks them every 30 seconds for anything that's gone wrong, and lets you fix what it finds without remembering the commands.

It's built to work just as well from a phone over SSH as from a wide desktop terminal: the layout changes with the screen, nothing needs Ctrl or Alt, and you can tap anything.

## Quick start

opsie runs on Linux (amd64 and arm64).

```bash
curl -fsSL https://raw.githubusercontent.com/aloglu/opsie/main/install.sh | sh
```

Then run `opsie`. The [script](install.sh) downloads the latest release, checks it against the release's checksums, and puts it in `~/.local/bin`; it doesn't need sudo or Go. To update later, run `opsie update`; to remove it, `opsie uninstall`.

More ways to install, and how to remove it, are in **[docs](docs/installing.md)**.

## What it does

- **Health checklist:** problems first, each with what it means and a button to fix it: a failed service, an unhealthy container, a full disk, a pending restart, a site that stopped answering.
- **Docker:** projects and containers with logs, a shell, restarts, image updates and clean-up of unused images.
- **Backups:** Docker apps backed up by hand or every night, encrypted, verified, restored in one step, and copied off the server over SSH or to S3-compatible storage.
- **Services and processes:** restart a failed service, read its log, stop or slow down a runaway process.
- **Storage:** every disk, what's using the space folder by folder, and what's safe to free.
- **Network:** traffic charts, a speed test, and your Cloudflare Tunnel with every hostname it serves.
- **Security and updates:** who can log in, what's exposed to the internet, the firewall, and pending updates.
- **AI agents:** how much Claude Code and Codex have been used on the server, and what OpenRouter or DeepSeek keys have spent.
- **Background checks:** an optional service that watches while you're away and alerts through ntfy, Telegram or a webhook.
- **Phone and desktop:** the layout follows the screen, nothing needs Ctrl or Alt, and everything can be tapped. Six themes for dark or light terminals.

## Documentation

- [Guides](docs/README.md): installing, using each tab, alerts, configuration.
- [Development](docs/development.md).

## License

Released under the [MIT License](https://github.com/aloglu/opsie/blob/e07883e4e69b5504eba9ea2a3c728a295982e0b3/LICENSE).
