# Background checks and alerts

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

## Login summary

`opsie motd` prints a few lines about the server: how it is, its uptime, memory and disk, and up to three things that need attention. It reads what the background checks found, so it takes a few milliseconds. To see it every time you log in, choose **Show summary at login** in Background checks, or run `opsie motd --install` (`--remove` takes it out). It adds a marked block to `~/.profile` (or `~/.bash_profile`, `~/.zprofile`, or fish's `conf.d`, whichever your shell reads at login) and doesn't touch anything else there.
