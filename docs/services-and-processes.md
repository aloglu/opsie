# Services and processes

## Services

The **Services** tab shows systemd's view: failed services first, then what's running with its memory and uptime, the timers with when they run next, and enabled services that aren't running right now. Open one to restart, reload, start or stop it, choose whether it starts at boot, clear a failure, or read its **log** from the journal in the same live viewer containers use. Timers can be run right away.

Below the timers, **Cron** lists the jobs in `/etc/crontab`, `/etc/cron.d`, users' own crontabs and the `cron.daily`-style folders, each with its schedule in words, when it runs next, who it runs as and the file it's in. It's for looking only. If cron isn't installed or isn't running, it says so, since then none of them run: Debian leaves these files around even when systemd timers do the work.

opsie won't stop or disable the services that keep the server reachable (SSH, networking, DNS, dbus, logind, the journal); it only restarts them. Restarting Docker asks first, because it briefly stops every container.

## Processes

The **Processes** tab has the CPU and memory charts at the top; the selection starts on CPU, and opening either shows its last day and week. Under them it lists the busiest processes by CPU or memory (`s`, or tap the switch), with the service or container each one belongs to. Open one to see its command line and start time, ask it to stop or force it to, or lower its priority so it yields to everything else. If it belongs to a service or container, there's a link straight to that, since stopping the service is usually the better move.

opsie checks the process is still the same one before signalling it (a reused PID won't be hit), and won't touch PID 1, kernel threads, its own session, or the main process of a service the server needs.
