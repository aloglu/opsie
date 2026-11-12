# Docker

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
