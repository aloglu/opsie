# Configuration

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
