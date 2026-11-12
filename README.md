# opsie

A dashboard for your server that lives in the terminal. opsie keeps an eye on CPU, memory, disks, network, containers, services and security settings, checks them every 30 seconds for anything that's gone wrong, and lets you fix what it finds without remembering the commands.

It's built to work just as well from a phone over SSH as from a wide desktop terminal: the layout changes with the screen, nothing needs Ctrl or Alt, and you can tap anything.

## Install

opsie runs on Linux (amd64 and arm64).

```bash
curl -fsSL https://raw.githubusercontent.com/aloglu/opsie/main/install.sh | sh
```

The [script](install.sh) downloads the prebuilt binary from the latest release, checks it against the release's checksums, and puts it in `~/.local/bin`. It doesn't need sudo or Go, and it tells you if that folder isn't on your PATH yet.

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

## What it shows

The **Health** checklist puts problems first:

| Check | Looks at |
|---|---|
| CPU load, memory, swap | load per core and available memory against your thresholds |
| Disks | space and inodes on every real filesystem |
| Services | systemd units in the failed state |
| Containers | unhealthy containers, restart loops, containers that exited with an error |
| Restart | whether an installed update (usually a kernel) is waiting for a reboot, and which packages |
| SSH login | whether passwords or root password logins are allowed |
| SSH keys | how many keys can log in, per user |
| Firewall | whether ufw or nftables is active, and how many rules it has |
| Clock | whether network time sync is on and synchronized |
| Passwordless sudo | whether your account gets root without a password |
| Updates | security updates waiting, stale package lists, whether automatic updates are on, services still running replaced libraries |
| Failed logins | SSH login attempts in the last day, and whether anything slows guessing down |
| Docker ports | each container with ports open to the internet, which ufw can't filter (databases count as problems) |
| Container logs | logs with no size limit, and logs that have grown large |
| Nightly backups | whether last night's backups were made, checked and copied off the server |
| Tunnel | whether cloudflared runs and is connected, and whether the apps behind its hostnames answer |
| Sites | whether each public hostname, and any extra site, answers visitors quickly and with a valid certificate |
| Background checks | whether the background service is on and checking, and whether alerts get through |
| New images | containers whose image tag now points at a newer image (news, never an alert) |

Around it are live cards for CPU, memory, network and disks with charts, and a system card with the time, uptime, OS, kernel and who's logged in. With background checks on, opening CPU, memory, swap, network or a disk also shows the **last 24 hours and 7 days**, with the low, average and high; a disk that creeps up slowly gets a zoomed scale so the climb shows.

Open any line to see what it means and what you can do about it: restart a failed service or clear its failure, restart or start a container, turn on time sync, or restart the server, now or at a quiet time like 04:00 (either asks first, and a scheduled restart can be cancelled until it happens).

Open a disk and choose **What's using the space**: the biggest folders first, each with its size and a bar for its share of the folder it's in, and each one opening the folders inside it. It measures only when you ask, at the lowest priority, and stays on that disk. **Free up space** lists only what's safe to remove, with how much each frees: trimming the system log to 500 MB, clearing packages apt already installed, removing packages nothing needs anymore (named in the confirmation, and never the running kernel), and Docker's clean-up. Docker's own folders point you at Docker's clean-up instead of being deleted.

### Docker

The **Docker** tab groups containers by Compose project, with each one's status, health, CPU and memory, refreshed every few seconds. Open a container to see its image, ports, restart count and resource use, including how fast it's sending and receiving over the network (wide screens show that in the list too) and how big its log is, and to:

- read its **logs**, live, with errors and warnings picked out (scroll back, or `w` to stop wrapping)
- open a **shell** inside it
- restart, stop, start or remove it

Open a project to restart, start or stop all of it, or to **pull newer images** or **rebuild** and recreate its containers using the compose file it was started from. Before offering either, opsie checks there's room: if the disk is too full for new images it says so and points you at freeing space instead, and if space or memory is tight it warns you in the confirmation.

The card at the top shows what Docker's images and build cache take up, and how much of that no container uses. Opening it lists those unused images with what each would free, and says what every button does before you press it: **Clean up** removes leftover image layers and the build cache, which nothing can use again; **Remove unused images** removes the images no container uses, usually older versions left behind by updates and most of the space (they're named in the confirmation; one that's needed later is downloaded again, or built again if it was built on the server); **Deep clean** also removes stopped containers (by name, in the confirmation) and unused networks, and the images that leaves unused. Volumes are never touched.

Every few hours opsie asks the registries your images come from (Docker Hub, GitHub and others) whether their tags point at a **newer image**, and marks the project when one is out. It only compares digests, never downloads, and skips images built here; turn it off in **Settings → Images**.

Each project also shows the public addresses that lead to it (see below) and has **Open folder**, which opens your own shell in the project's folder, as you rather than root, for editing its files. Folders in `~/apps` (or `apps_dir`) that have a compose file but nothing running are listed under **Not started**, with a **Start** that runs `docker compose up -d` there.

The tab name gets a mark when a container needs attention, so you see it from Overview too.

**Ports open to the internet.** Docker writes its own firewall rules, so a port in a compose file like `"5432:5432"` is reachable from anywhere even when ufw says it's blocked. The checklist names each container that does this (a database port is a problem, anything else a warning). **Only this server** rewrites that one line of the compose file to `127.0.0.1:5432:5432`, keeping a copy of the old file next to it, and recreates the container; it puts the old file back if that fails. For something that should be public, like a game server, choose **It's meant to be public** and it isn't flagged again.

**Container logs.** Docker keeps everything a container prints, and by default never trims it. **Limit logs** sets Docker's default to 10 MB × 3 files per container in `/etc/docker/daemon.json` (checked with `dockerd --validate` before it's saved). Docker can't change a container that already exists, so the checklist then takes you through the rest: **Restart Docker** so it reads the setting, then **Recreate** each app, which makes its containers again from the same images and keeps its volumes. A log that's already big can be emptied straight away, while the container keeps running.

#### Backups

Open a project and choose **Backups** to save the app into one file: its compose folder and its Docker volumes, which together hold everything it keeps. **Back up now** stops the app while it's copied, so databases are saved whole, and starts it again; **Back up without stopping** skips the downtime. Folders mounted from outside the app's folder aren't included, and the screen names them. Backups go to `~/opsie-backups` (a folder in your home, or under `/mnt`, `/media`, `/srv` or `/var/backups`), belong to you so you can copy them off the server, and the last five of each app are kept. Both are in Settings.

**Nightly backups.** Turn on **Back up every night** for an app (or list it under `nightly`) and the background service backs it up at `at` (03:30), checks the new file opens, and copies it off the server if you set that up. The checklist warns when a night was missed or a backup failed, so you hear about it.

**Check this backup** reads a backup end to end without restoring it: the file matches the checksum written next to it when it was made, and with the key at hand, that it decrypts and unpacks whole.

**Copies off the server.** A backup that lives only on the server is gone with the server. Set **Settings → Off-server copies** and every backup, nightly or by hand, is also copied:

- over **SSH**, to `user@host:folder` (with `ssh_port`), using your own SSH key and known_hosts, so the other machine has to be one you've connected to before; or
- to **S3-compatible storage**: Cloudflare R2, Backblaze B2, AWS S3, Wasabi, or your own. For R2, the endpoint is `https://ACCOUNT_ID.r2.cloudflarestorage.com` and the region `auto`. Each upload is signed and goes over https.

A backup that isn't encrypted is copied as it is, and the Backups screen says so.

**Restore** puts a backup back: the app stops, its folder and volumes are swapped for the backup's, and it starts again. Nothing is deleted: what was there is set aside next to it, and the Backups screen offers to delete it once you're happy. The backup is unpacked in full before anything is touched, so a damaged file or a wrong key changes nothing.

**Encryption** (Settings → Backups) uses [age](https://age-encryption.org). Make a key and opsie keeps the secret key in `~/.config/opsie/backup-key.txt`, so restores here just work; save a copy somewhere else, like a password manager, because a backup you copied off the server is useless without it. You can then remove it from the server, and restores ask you to paste it. Or use the public key of an age key you already have. Making a new key keeps the old one in the file, so older backups still open.

#### Cloudflare Tunnel

If `cloudflared` runs here, as a container or a systemd service, the Network tab has a **Tunnel** card (and each Docker app shows the hostnames that lead to it): whether it's connected and to which Cloudflare locations, and every public hostname with the app it leads to. opsie reads these from cloudflared's own log and status page, so it needs no Cloudflare account access. Open a hostname to see whether requests to it are failing and why ("nothing is listening on the app's port"), and jump to its app. The checklist warns when the tunnel stops, loses its connection, or can't reach an app behind it, so background checks alert you when a site goes down.

opsie also **visits each hostname** every two minutes, from the internet through Cloudflare, the way a visitor would. A site that answers with a server error, a Cloudflare error page, nothing at all, or a bad certificate is a problem; one that takes longer than `slow_seconds` is a warning. Each failing site gets its own line in the checklist, so a second site going down alerts again. A login page (401) counts as up. Add sites that aren't behind the tunnel under **Settings → Sites**.

Hostnames show up for tunnels managed from the Cloudflare dashboard (the ones started with a token). A tunnel configured with a local `config.yml` still gets its status, but opsie doesn't read the file.

### Services

The **Services** tab shows systemd's view: failed services first, then what's running with its memory and uptime, the timers with when they run next, and enabled services that aren't running right now. Open one to restart, reload, start or stop it, choose whether it starts at boot, clear a failure, or read its **log** from the journal in the same live viewer containers use. Timers can be run right away.

Below the timers, **Cron** lists the jobs in `/etc/crontab`, `/etc/cron.d`, users' own crontabs and the `cron.daily`-style folders, each with its schedule in words, when it runs next, who it runs as and the file it's in. It's for looking only. If cron isn't installed or isn't running, it says so, since then none of them run: Debian leaves these files around even when systemd timers do the work.

opsie won't stop or disable the services that keep the server reachable (SSH, networking, DNS, dbus, logind, the journal); it only restarts them. Restarting Docker asks first, because it briefly stops every container.

### Processes

The **Processes** tab has the CPU and memory charts at the top, where the screen is wide enough, and lists the busiest processes by CPU or memory (`s`, or tap the switch), with the service or container each one belongs to. Open one to see its command line and start time, ask it to stop or force it to, or lower its priority so it yields to everything else. If it belongs to a service or container, there's a link straight to that, since stopping the service is usually the better move.

opsie checks the process is still the same one before signalling it (a reused PID won't be hit), and won't touch PID 1, kernel threads, its own session, or the main process of a service the server needs.

### Storage

The **Storage** tab is where the disks live. Each one has a card with a bar as wide as the card, what's used and free, its filesystem and device, and how many of its file slots (inodes) are taken; opening it shows the last day and week, and leads to what's using the space and what can be freed. **Biggest folders** measures the main disk when you open the tab (at the lowest priority, and kept for ten minutes) and draws each folder with its size and a bar for its share; open one to go into it. **Free up space** lists what's safe to remove with how much each frees, and the **Docker** card shows its share and leads to its clean-up.

### Network

The **Network** tab shows traffic in and out with taller charts than the Overview's (open it for the last day and week), the interfaces with their addresses and totals, a **speed test**, and the [Cloudflare Tunnel](#cloudflare-tunnel) with its hostnames, if one runs here. The test measures the server's connection against the nearest Cloudflare data center: the round trip, then download and upload on four connections each, for eight seconds or until it has moved 500 MB down and 250 MB up, whichever comes first. It only runs when you ask, shows its progress as it goes, and remembers the last twenty results.

### Agents

The **Agents** tab shows how much the AI coding agents on the server have been used: [Claude Code](https://claude.com/claude-code) and Codex, when their folders are in your home. For each: the plan's limits with when they start over, tokens and sessions today and over seven days with a bar per day, and the model used last. Opening one splits the tokens into sent, written and cached, and by model.

It's all read from the session logs the agents keep (`~/.claude/projects`, `~/.codex/sessions`); nothing is sent anywhere. Codex writes its plan limits there too, as of when it was last used. Claude Code doesn't, so its limits are off until you turn on **Claude plan limits** in Settings: then opsie reads the login Claude Code keeps in `~/.claude` and asks `api.anthropic.com` with it, at most every five minutes while the tab is open. The login is only read, never changed or sent anywhere else. Anthropic doesn't document this address, so it may stop answering one day; the rest of the tab doesn't depend on it.

Services paid by use show what a key has spent once opsie has the key: **OpenRouter** (spent today, this week and this month, and how much of the key's limit or of your credits is gone) and **DeepSeek** (the balance left). Add a key from the tab's **Services** card or in Settings; it's kept in `config.toml`, asked about at most every five minutes while the tab is open, and sent only to the service it belongs to.

### Security

The **Security** tab gathers who can get in and what's exposed:

- **Access**: turn SSH password login off (only once you have a key, so you can't lock yourself out), allow root only with a key or not at all, and switch passwordless sudo on or off.
- **SSH keys**: every key that can log in, with the one you're using marked. Add a key by pasting it; opsie checks it's a public key and shows its fingerprint before saving. Remove keys, except the one you're logged in with.
- **Sessions**: who's logged in and from where. End any session but your own.
- **Firewall**: ufw's rules. Turn it on (SSH is allowed first), allow a port, block an address, delete a rule. The rule that lets SSH in can't be deleted.
- **Listening**: every port something listens on, and whether it's open, blocked by the firewall, or only reachable from the server itself. Ports Docker publishes are called out, because Docker bypasses ufw.
- **Logins**: the last few SSH logins, from where and with which key.
- **Failed logins**: attempts in the last day, the busiest addresses (block one with a tap), and fail2ban, which opsie can install and point at the SSH log.

SSH changes go into one file, `/etc/ssh/sshd_config.d/0-opsie.conf`, which sorts before the others so its values win. opsie checks the result with `sshd -t` before reloading, confirms with `sshd -T` that sshd uses the new values, and puts the old file back if anything fails. sudo changes are checked with `visudo` the same way.

### Updates

The **Updates** tab lists packages with a newer version, security updates first. Check for updates, install one or all of them (config files you've changed are kept, nothing is removed), and turn on automatic security updates. Below that is what's waiting for a restart: the server itself after a kernel update (restart now, or schedule it for a time you pick), and services still running a library an update replaced, each of which you can restart on its own.

### Background checks and alerts

opsie can keep checking when the dashboard is closed. Open **Background checks** in the checklist and choose **Turn on**. It installs a small system service, `opsie.service`, that runs the same checks every 30 seconds, keeps a history (shown in that item), and sends an alert when something goes wrong and again when it's fixed.

Alerts are careful not to cry wolf:

- a problem has to show up on two checks in a row, so a moment's spike doesn't page you, and the same goes for "it's fixed"
- when it first starts, you get one message listing what's wrong already, not one per problem
- something that gets worse (a warning that becomes a problem) alerts again; something still broken after `repeat_hours` gets a reminder
- during `quiet_hours`, only problems get through; warnings wait until the morning

Tell it where to send them in the **Settings** tab, or in `config.toml` by hand (the service reads the same file and picks up changes on its own), then use **Send a test alert**:

```toml
[notify]
ntfy_url = "https://ntfy.sh/pick-a-long-secret-name"   # the ntfy app, or your own server
ntfy_token = ""                  # for a protected ntfy server
telegram_token = ""              # a bot token from @BotFather
telegram_chat = ""               # the chat it posts in
webhook_url = ""                 # gets {"title","text","lines","level"} as JSON
level = "warn"                   # "crit" sends problems only
logins = "new"                   # SSH logins from new addresses; "all" or "off"
weekly = ""                      # a summary, like "sun 18:00"
quiet_hours = "23:00-08:00"
repeat_hours = 12                # 0 never reminds
```

With `logins = "new"` (the default) you also get an alert when someone logs in over SSH from an address that hasn't logged in before, with the account and key; `"all"` alerts on every login. The addresses it already knows are learned quietly the first time it runs, and forgotten after 90 days unused. Set `weekly = "sun 18:00"` for a **weekly summary**: the uptime, what came up during the week, what still needs attention, CPU and memory on average and at their highest, free disk space, and last night's backups. **Send this week's summary** in Settings shows you one now.

They also record CPU, memory, swap, load, network and disk use once a minute for eight days (about 1.5 MB), which the dashboard draws as history charts.

The service runs as root from its own copy of opsie in `/usr/local/libexec/opsie`, which only root can change, so it never runs a file your account could swap. `opsie update` brings it up to date along with opsie, or the item offers to when the versions differ. Its history and status are in `/var/lib/opsie`.

### Login summary

`opsie motd` prints a few lines about the server: how it is, its uptime, memory and disk, and up to three things that need attention. It reads what the background checks found, so it takes a few milliseconds. To see it every time you log in, choose **Show summary at login** in Background checks, or run `opsie motd --install` (`--remove` takes it out). It adds a marked block to `~/.profile` (or `~/.bash_profile`, `~/.zprofile`, or fish's `conf.d`, whichever your shell reads at login) and doesn't touch anything else there.

## Using it

| Key | Does |
|---|---|
| `1`–`9`, `0` | switch tabs |
| `tab` / `shift+tab` | next / previous tab |
| `↑` `↓` / `j` `k` | move through the list; on wide screens, down a column and on to the next |
| `←` `→` / `h` `l` | move to the column beside it, when cards sit side by side |
| `g` `G` / `home` `end` | first / last item |
| `enter` | open the selected item |
| `PgUp` `PgDn` / `b` `f` `space` | scroll the page |
| `s` | on Processes, sort by CPU or memory |
| `r` | check everything again now |
| `?` | help |
| `q` | quit |

In a detail view, a few buttons sit in a row; more than fit become a menu under a rule, one per line or in even columns, with the chosen one highlighted across its cell. The arrows move between them the way they look: `←` `→` along a row (running on to the next), `↑` `↓` to the row above or below. With one row of buttons, `↑` `↓` scroll the text instead. `tab` also moves along them. `enter` or `space` presses it, `1`–`9` press one directly, `PgUp` `PgDn` scroll a long description, and `esc` (or `q`) goes back. When an action asks you to type something, `tab` or `↑` `↓` move between the field and **Cancel**.

In the log viewer, `↑` `↓` scroll (scrolling up stops following), `PgUp` `PgDn` scroll a page, `g` jumps to the oldest lines, `G` to the newest and follows them live, `w` toggles wrapping, `r` reads the log again, and `esc` goes back. Detail views open with a harmless button selected. Anything disruptive asks for confirmation with **Cancel** selected, and there the number keys are ignored, so a stray key or a double Enter can't go ahead by accident.

Everything is also a tap target: checklist lines, containers and cards open, buttons and tabs press, the hints at the bottom work, and swiping scrolls.

### On a phone

Any SSH client that passes touches through as mouse events works: Termius, Blink, JuiceSSH and others. In portrait the cards stack into one scrolling column. The Overview starts with every reading as a line of its own (CPU, memory, each disk, the network; tap one for its history or its tab), then the checklist. The tabs sit at the bottom within reach of your thumb, as a strip of the ones around the tab you're on: tap one, or the arrow at either end for the rest. A popup's actions are a menu of full rows to tap, and rows that would be cut off wrap onto a second line instead. Turn the phone sideways and the cards spread into columns and the tabs move to the top.

## How it gets root

opsie runs as you. Checks and actions that need root go through a small helper it starts once with `sudo -n opsie helper`, which answers a fixed list of requests (read the SSH config, list containers, restart a unit, and so on) and refuses anything else. It exits when opsie does.

If your sudo asks for a password, opsie opens with those checks marked as locked; open **Admin access** and choose **Unlock** to type your password once.

Every action is written to an audit log before it runs and again with its result (secrets like a pasted backup key are left out). **Settings → Background → Actions taken** lists them, newest first, with whether each worked; `opsie paths` shows where the file is.

## Configuration

The **Settings** tab changes everything below from inside opsie: pick a theme, paste an ntfy topic or a Telegram token, set quiet hours or thresholds. Each value is checked as you type, and saving changes only that line of `config.toml`, keeping your comments and the rest of the file as they were. Tokens show masked.


`opsie paths` shows where `config.toml` lives. Every setting is optional:

```toml
refresh_seconds = 2     # how often live stats update
check_seconds = 30      # how often the health checks run
theme = "opsie"         # opsie, flexoki, moss, ocean, dusk or mono
background = "auto"     # auto asks the terminal; or say dark or light
apps_dir = "~/apps"     # where your apps' folders are

[backup]
dir = "~/opsie-backups" # where app backups go
keep = 5                # backups kept per app
encrypt_to = ""         # an age public key; set it in Settings → Backups
nightly = []            # apps backed up every night, like ["wiki"]
at = "03:30"            # when
stop = true             # stop each app while it's copied
ssh = ""                # copy backups to user@host:folder
ssh_port = 22

[backup.s3]             # or to S3-compatible storage
endpoint = ""           # like "https://s3.eu-central-003.backblazeb2.com"
region = ""             # "auto" for R2
bucket = ""
prefix = ""             # a folder in the bucket
key_id = ""
secret = ""

[docker]
public_ports = []       # ports meant to be public, like ["game-server:27015/udp"]

[images]
check = true            # ask registries about newer images every 6 hours

[agents]
claude_limits = false   # ask Anthropic for the Claude plan's limits, with Claude Code's login
openrouter_key = ""     # show what an OpenRouter key has spent
deepseek_key = ""       # show a DeepSeek account's balance

[sites]
check = true            # visit every tunnel hostname every two minutes
extra = []              # more addresses, like ["https://example.com"]
slow_seconds = 5

[thresholds]            # percentages, except load (per CPU core)
disk_warn = 85
disk_crit = 95
memory_warn = 90
memory_crit = 97
swap_warn = 60
load_warn = 1.5
load_crit = 3
```

`opsie themes` shows what each theme looks like. A theme is more than an accent color: it has its own colors for every part, its own greys, and its own shapes for cards, bars and charts.

| Theme | Colors | Shapes |
|---|---|---|
| `opsie` | teal, cool slate greys | rounded cards, thin bars |
| `flexoki` | the inks of [Flexoki](https://stephango.com/flexoki) on warm paper greys, a little quieter | square cards, solid bars |
| `moss` | green, lichen greys | rounded cards, soft half-height bars |
| `ocean` | blue, blue-greys, a little stronger | heavy lines, solid bars |
| `dusk` | pink and violet, mauve greys, the strongest | dashed lines, dotted charts |
| `mono` | none but the statuses | square cards |

The selected line is drawn as a band in the theme's selection color. When the selection is a whole card (a disk, a chart, an agent), only its border and title take that color, so nothing is drawn over the chart or bar inside; on a line that ends in a bar, the band stops before the bar.

Within a theme every part has a color of its own (the selection, the tabs and keys, each of the CPU, memory, disk and network charts, and the statuses), so a color only ever means one thing. Colors are mixed for your terminal's background, which opsie asks the terminal about: bright and full on a dark one, deep on a light one. If your terminal doesn't answer, they fall back to a middle brightness that reads on both but is duller; set `background` to `dark` or `light` to fix that. Text and the background stay your terminal's own. Changes to `config.toml` are picked up while opsie runs.

## Uninstall

If you set up the login summary, run `opsie motd --remove`. If you turned on background checks, turn them off from the dashboard first (or `sudo systemctl disable --now opsie && sudo rm /etc/systemd/system/opsie.service && sudo rm -r /usr/local/libexec/opsie /var/lib/opsie`). Then:

```bash
rm ~/.local/bin/opsie
rm -rf ~/.config/opsie ~/.local/state/opsie
```

## Development

```bash
make run        # build and open the dashboard
make test       # run the tests
make check      # everything CI runs
make daemon     # run the background checks in the foreground (Ctrl+C stops)
make preview W=45 H=32          # print one frame at a size, with live data
make preview W=45 H=32 SCREEN=detail     # also help, docker, prune,
                                         # container:NAME, project:NAME, logs:NAME,
                                         # security[:ID], updates[:ID], services[:ID],
                                         # services:logs:UNIT, procs[:pid:N],
                                         # storage, network, agents, settings, explore:/
```

Builds from a checkout are development builds. They keep their own settings and audit log in `~/.config/opsie-dev` and `~/.local/state/opsie-dev`, say **dev** in the header, never update themselves, and never install the background service: `make daemon` runs it by hand, keeping its status in `/run/opsie-dev` where the dev dashboard looks, so working on opsie never touches the copy you installed from a release.

The tests render every screen at sizes from 30×10 up to 200×60 and fail if any line is wider or taller than the terminal, so the phone layout can't quietly break.

### Releasing

1. Write the release on GitHub with a new tag (`v0.2.0`). Put the changes in the notes as a bullet list; `opsie update` shows those bullets to everyone updating.
2. Publish it. The release workflow tests, builds the Linux binaries with GoReleaser, and attaches them with `checksums.txt`.

## License

MIT
