# The Overview and the Health checklist

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

Above it are live cards for CPU, memory, network and disks with charts: in one row where the screen is wide enough for four, two by two where it isn't, and always first on the page. Beside or under the checklist is a system card with the time, uptime, OS, kernel and who's logged in. The **Disks** card sits with the other charts: a bar for how full each disk is and, under them, the totals since boot and charts of what's being read (R) and written (W) right now, with how busy the busiest disk is in the corner. On a small screen the cards become one, **Now**: a line each for CPU, memory, swap, the main disk (another disk appears only once it's filling up), and each direction of the network and the disks' activity, all drawn the same way: a small chart of the last while and the reading now. With background checks on, opening CPU, memory, swap, network, the disks' activity or a disk also shows the **last 24 hours and 7 days**, with the low, average and high; a disk that creeps up slowly gets a zoomed scale so the climb shows.

Open any line to see what it means and what you can do about it: restart a failed service or clear its failure, restart or start a container, turn on time sync, or restart the server, now or at a quiet time like 04:00 (either asks first, and a scheduled restart can be cancelled until it happens).
