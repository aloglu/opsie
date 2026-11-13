# Configuration

The **Settings** tab changes everything below from inside opsie: pick a theme, paste an ntfy topic or a Telegram token, set quiet hours or thresholds. Each value is checked as you type, and saving changes only that line of `config.toml`, keeping your comments and the rest of the file as they were. Tokens show masked. Its sections are in alphabetical order. Under **opsie**, **Version** shows which opsie is running and whether a newer one is out; open it to check now instead of waiting for the daily check, and to update.


`opsie paths` shows where `config.toml` lives. Every setting is optional:

```toml
refresh_seconds = 2     # how often live stats update
check_seconds = 30      # how often the health checks run
theme = "opsie"         # opsie, terminal or mono
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

`opsie themes` shows what each theme looks like. There are three, and they differ in where their colors come from:

| Theme | Colors | Shapes |
|---|---|---|
| `opsie` | opsie's own: blue, violet and cyan lead, with pink, teal and orange for the charts | rounded cards, thin bars |
| `terminal` | your terminal's own sixteen colors, so opsie matches whatever theme the terminal has | square cards, solid bars |
| `mono` | none but the statuses | square cards, solid bars |

The selected line is drawn as a band in the theme's selection color. When the selection is a whole card (a disk, a chart, an agent), only its border and title take that color, so nothing is drawn over the chart or bar inside; on a line that ends in a bar, the band stops before the bar.

In `opsie` a color only ever means one thing: the selection, the tabs and keys, the CPU, the memory, the disks, the network and each status have their own. Readings that come in twos are two shades of one color: in and out, read and written. Its colors are mixed for your terminal's background, which opsie asks the terminal about: bright on a dark one, deep on a light one. If your terminal doesn't answer, they fall back to a middle brightness that reads on both but is duller; set `background` to `dark` or `light` to fix that.

`terminal` has only the six hues a terminal theme defines, so some parts share one: the disks are green like "ok", the network blue like "info". The selection is the terminal's own text and background swapped. It's also the theme to pick where a terminal can't show more than sixteen colors.

Text and the background stay your terminal's own. Changes to `config.toml` are picked up while opsie runs.
