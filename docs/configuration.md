# Configuration

The **Settings** tab changes everything below from inside opsie: pick a theme, paste an ntfy topic or a Telegram token, set quiet hours or thresholds. Each value is checked as you type, and saving changes only that line of `config.toml`, keeping your comments and the rest of the file as they were. Tokens show masked. Its sections are in alphabetical order. Under **opsie**, **Version** shows which opsie is running and whether a newer one is out; open it to check now instead of waiting for the daily check, and to update.


`opsie paths` shows where `config.toml` lives. Every setting is optional:

```toml
refresh_seconds = 2     # how often live stats update
check_seconds = 30      # how often the health checks run
theme = "opsie"         # opsie, mono or a color scheme; see below
background = "auto"     # auto asks the terminal; or say dark or light
apps_dir = "~/apps"     # where your apps' folders are

[charts]
style = "bars"          # bars, braille, line or dense
cpu = "same"            # a style of its own for one reading, or the same as the rest
memory = "same"
network = "same"
disk = "same"

[backup]
dir = "~/opsie-backups" # where app backups go
keep = 5                # backups kept per app
encrypt_to = ""         # an age public key; set it in Settings → Backups
nightly = []            # apps backed up on a schedule, like ["wiki"]; none means only when you ask
every_days = 1          # days between those backups: 1 is every night, 7 once a week
at = "03:30"            # when
stop = true             # stop each app while it's copied
keep_remote = 0         # copies of each app kept off the server; 0 keeps them all
ssh = ""                # copy backups to user@host:folder, a folder of this server's own
ssh_port = 22

[backup.s3]             # or to S3-compatible storage
endpoint = ""           # like "https://s3.eu-central-003.backblazeb2.com"
region = ""             # "auto" for R2
bucket = ""
prefix = ""             # a folder in the bucket, this server's own
key_id = ""             # a key that can write; keep_remote also needs list and delete
secret = ""

[docker]
public_ports = []       # ports meant to be public, like ["game-server:27015/udp"]

[images]
check = true            # ask registries about newer images every 6 hours

[agents]
claude_limits = false   # ask Anthropic for the Claude plan's limits, with Claude Code's login
openrouter_key = ""     # show what an OpenRouter key has spent; several as ["sk-or-…", "work=sk-or-…"], a name before = is optional
deepseek_key = ""       # show a DeepSeek account's balance
check = true            # warn and alert when a key runs low, runs out or is refused
low_percent = 10        # percent of a key's limit or the account's credits left that counts as low; 0 for never
low_balance = 1         # a balance, in the account's currency, that counts as low; 0 for never

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

`opsie themes` shows what each theme looks like. Two are opsie's own:

| Theme | Colors | Shapes |
|---|---|---|
| `opsie` | opsie's own: blue, violet and cyan lead, with pink, teal and orange for the charts | rounded cards, thin bars |
| `mono` | none but the statuses | square cards, solid bars |

The other nine are well-known color schemes, with their colors as the schemes have them:

| Theme | Colors | Shapes |
|---|---|---|
| `catppuccin` | Catppuccin Mocha: mauve, blue, peach and teal pastels | rounded cards, thin bars |
| `cyberpunk` | neon cyan, magenta and yellow at full strength | double-lined cards, block bars |
| `dracula` | Dracula: purple, pink, cyan and green | rounded cards, thin bars |
| `gruvbox` | Gruvbox: warm orange, yellow and olive green | square cards, solid bars |
| `nord` | Nord: cool, quiet blues with a few warm colors | square cards, solid bars |
| `ocean` | sky blue, sea green and sand | rounded cards, thin bars |
| `retro` | an amber screen, with green for what's well | double-lined cards, block bars |
| `rose-pine` | Rosé Pine: rose, iris, gold and pine | rounded cards, thin bars |
| `tokyo-night` | Tokyo Night: blue, purple and cyan | rounded cards, thin bars |

The selected line is drawn as a band in the theme's selection color. When the selection is a whole card (a disk, a chart, an agent), only its border and title take that color, so nothing is drawn over the chart or bar inside; on a line that ends in a bar, the band stops before the bar.

In `opsie` a color only ever means one thing: the selection, the tabs and keys, the CPU, the memory, the disks, the network and each status have their own. Readings that come in twos are two shades of one color: in and out, read and written. Its colors are mixed for your terminal's background, which opsie asks the terminal about: bright on a dark one, deep on a light one. If your terminal doesn't answer, they fall back to a middle brightness that reads on both but is duller; set `background` to `dark` or `light` to fix that.

A color scheme has fewer colors than opsie has parts, so in those some parts share one: the disks are usually the color of "ok", and the network the color of a warning. The selection still never shares a color with a chart, and a reading that changes color when it runs high never shares one with a warning. The schemes are all made for dark terminals. On a dark one their colors are exact; on a light one each keeps its hue and is deepened until it reads. They look most like themselves in a terminal that uses the same scheme, since the background and the text are the terminal's. In `retro` the selection is the terminal's text and background swapped.

### Charts

`[charts]` picks how the charts are drawn. `style` is for all of them, and `cpu`, `memory`, `network` and `disk` give one reading a style of its own:

| Style | What it draws |
|---|---|
| `bars` | solid bars, one reading to a column |
| `braille` | a fine dotted line |
| `line` | a drawn line, rounded where the theme's cards are |
| `dense` | bars at twice the detail: two readings to a column, in half-height steps |

`memory` covers swap too, and `disk` covers what the disks read and write and how full they are. The styles apply to the live charts and to the last day and week. A chart one row tall, like the ones on a small screen's **Now** card and beside each container, is always bars. In every style a reading that passes its warning turns the warning's color where it does. The styles other than `bars` are drawn with [ntcharts](https://github.com/NimbleMarkets/ntcharts).

Text and the background stay your terminal's own. Changes to `config.toml` are picked up while opsie runs.
