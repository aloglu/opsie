# Network

The **Network** tab opens with **Download** and **Upload**, charts of the traffic in and out right now, each on its own scale with its peak and total since boot; open either for the last day and week. Under them are the interfaces with their addresses and totals, a **speed test**, and the [Cloudflare Tunnel](#cloudflare-tunnel) with its hostnames, if one runs here. The test measures the server's connection against the nearest Cloudflare data center: the round trip, then download and upload on four connections each, for eight seconds or until it has moved 500 MB down and 250 MB up, whichever comes first. It only runs when you ask, shows its progress as it goes, and remembers the last twenty results.

## Cloudflare Tunnel

If `cloudflared` runs here, as a container or a systemd service, the Network tab has a **Tunnel** card (and each Docker app shows the hostnames that lead to it): whether it's connected and to which Cloudflare locations, and every public hostname with the app it leads to. opsie reads these from cloudflared's own log and status page, so it needs no Cloudflare account access. Open a hostname to see whether requests to it are failing and why ("nothing is listening on the app's port"), and jump to its app. The checklist warns when the tunnel stops, loses its connection, or can't reach an app behind it, so background checks alert you when a site goes down.

opsie also **visits each hostname** every two minutes, from the internet through Cloudflare, the way a visitor would. A site that answers with a server error, a Cloudflare error page, nothing at all, or a bad certificate is a problem; one that takes longer than `slow_seconds` is a warning. Each failing site gets its own line in the checklist, so a second site going down alerts again. A login page (401) counts as up. Add sites that aren't behind the tunnel under **Settings → Sites**.

Hostnames show up for tunnels managed from the Cloudflare dashboard (the ones started with a token). A tunnel configured with a local `config.yml` still gets its status, but opsie doesn't read the file.
