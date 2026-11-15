# Backups

Open a project and choose **Backups** to save the app into one file: its compose folder and its Docker volumes, which together hold everything it keeps. **Back up now** stops the app while it's copied, so databases are saved whole, and starts it again; **Back up without stopping** skips the downtime. Folders mounted from outside the app's folder aren't included, and the screen names them. Backups go to `~/opsie-backups` (a folder in your home, or under `/mnt`, `/media`, `/srv` or `/var/backups`), belong to you so you can copy them off the server, and the last five of each app are kept. Both are in Settings.

**Scheduled backups.** Nothing is backed up on its own until you ask for it: **Back up now** makes one whenever you like, and that's all there is by default. Turn on **Back up on a schedule** for an app (or list it under `nightly`) and the background service backs it up at `at` (03:30), checks the new file opens, and copies it off the server if you set that up. **Days between** (`every_days`) spaces them out: 1 is every night, 7 once a week. The checklist warns when one was missed or failed, so you hear about it.

**How much space it takes.** An app's Backups screen says what its data takes now, before any backup is made, and what its kept backups add up to; a backup is compressed, so it's usually smaller than the data. **Back up everything** shows the total of all backups on the server. Where copies go off the server, the screen also says what each copy is, what that comes to in a week on the schedule, and whether anything clears the old ones.

**Check this backup** reads a backup end to end without restoring it: the file matches the checksum written next to it when it was made, and with the key at hand, that it decrypts and unpacks whole.

**Copies off the server.** A backup that lives only on the server is gone with the server. Set **Settings → Off-server copies** and every backup, nightly or by hand, is also copied:

- over **SSH**, to `user@host:folder` (with `ssh_port`), using your own SSH key and known_hosts, so the other machine has to be one you've connected to before. The folder is an absolute path, or one inside that user's home (`backups/harbor` or `~/backups/harbor`); or
- to **S3-compatible storage**: Cloudflare R2, Backblaze B2, AWS S3, Wasabi, or your own. For R2, the endpoint is `https://ACCOUNT_ID.r2.cloudflarestorage.com` and the region `auto`. Each upload is signed and goes over https.

A backup that isn't encrypted is copied as it is, and the Backups screen says so.

By default opsie never deletes anything off the server, so the copies there keep adding up: a full copy of every scheduled app, every time. If you pay for that space or are on a free plan, set **Keep off the server** (`keep_remote`) to how many copies of each app to keep. After a new copy arrives, opsie deletes the older ones there, over SSH or from the bucket. It only ever deletes files it put there itself, and never the newest ones you asked it to keep.

Give each server a folder of its own, over SSH or as the bucket's `prefix`. Two servers that share one, and both have an app with the same name, would clear each other's copies of it.

The S3 key only has to write to the bucket, which is safest: a stolen key then can't read or delete your backups. Keep off the server also needs it to list and delete, and fetching backups onto a new server needs it to list and read.

**Restore** puts a backup back: the app stops, its folder and volumes are swapped for the backup's, and it starts again. Nothing is deleted: what was there is set aside next to it, and the Backups screen offers to delete it once you're happy. The backup is unpacked in full before anything is touched, so a damaged file or a wrong key changes nothing.

**Moving to another server, or getting one back.** Two screens under Settings → Backups do every app in one go.

On the server you're leaving, **Back up everything** backs up each app in turn, copies each off the server if that's set up, and lists any folder an app mounts from outside its own, which isn't in its backup. With encryption on, a copy of opsie's own settings goes along, encrypted like the rest; they hold tokens and keys, so they never travel unencrypted.

On the new server, install Docker and opsie, then open **Restore from backups** (the Docker tab offers it too while nothing runs there):

1. Get the backups into the backup folder. **Fetch** brings the newest backup of each app from S3 or over SSH, once you've said where they are under Off-server copies. Over SSH opsie uses your own key or forwarded agent and only connects to a machine already in your `known_hosts`. Or copy the files there yourself.
2. **Restore** puts every app that isn't on the server yet back from its newest backup and starts it, one at a time; one that fails doesn't stop the rest. If the backups are encrypted and the key isn't on the server, it asks you to paste the secret key once, and afterwards offers to keep it there.
3. **Bring back opsie's settings**, if a copy came along. The settings that were on the new server are kept beside them as `config.toml.before-restore`.

An app's folder goes back where it was. If that place can't exist on the new server, as when its user has another name, it goes into the new server's `apps_dir` instead; the screen shows where each will land before you confirm. A **Cloudflare Tunnel** is restored but not started: while it still runs on the old server, starting it on the new one would send visitors to both. Stop it on the old server, then start it from the Docker tab.

A restore doesn't bring over folders mounted from outside an app's own, Docker itself, the firewall, SSH settings or system services. An image an app builds is built again from its folder, and one tagged `latest` may be newer than what the old server ran.

**Encryption** (Settings → Backups) uses [age](https://age-encryption.org). Make a key and opsie keeps the secret key in `~/.config/opsie/backup-key.txt`, so restores here just work; save a copy somewhere else, like a password manager, because a backup you copied off the server is useless without it. You can then remove it from the server, and restores ask you to paste it. Or use the public key of an age key you already have. Making a new key keeps the old one in the file, so older backups still open.
