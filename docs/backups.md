# Backups

Open a project and choose **Backups** to save the app into one file: its compose folder and its Docker volumes, which together hold everything it keeps. **Back up now** stops the app while it's copied, so databases are saved whole, and starts it again; **Back up without stopping** skips the downtime. Folders mounted from outside the app's folder aren't included, and the screen names them. Backups go to `~/opsie-backups` (a folder in your home, or under `/mnt`, `/media`, `/srv` or `/var/backups`), belong to you so you can copy them off the server, and the last five of each app are kept. Both are in Settings.

**Nightly backups.** Turn on **Back up every night** for an app (or list it under `nightly`) and the background service backs it up at `at` (03:30), checks the new file opens, and copies it off the server if you set that up. The checklist warns when a night was missed or a backup failed, so you hear about it.

**Check this backup** reads a backup end to end without restoring it: the file matches the checksum written next to it when it was made, and with the key at hand, that it decrypts and unpacks whole.

**Copies off the server.** A backup that lives only on the server is gone with the server. Set **Settings → Off-server copies** and every backup, nightly or by hand, is also copied:

- over **SSH**, to `user@host:folder` (with `ssh_port`), using your own SSH key and known_hosts, so the other machine has to be one you've connected to before; or
- to **S3-compatible storage**: Cloudflare R2, Backblaze B2, AWS S3, Wasabi, or your own. For R2, the endpoint is `https://ACCOUNT_ID.r2.cloudflarestorage.com` and the region `auto`. Each upload is signed and goes over https.

A backup that isn't encrypted is copied as it is, and the Backups screen says so.

**Restore** puts a backup back: the app stops, its folder and volumes are swapped for the backup's, and it starts again. Nothing is deleted: what was there is set aside next to it, and the Backups screen offers to delete it once you're happy. The backup is unpacked in full before anything is touched, so a damaged file or a wrong key changes nothing.

**Encryption** (Settings → Backups) uses [age](https://age-encryption.org). Make a key and opsie keeps the secret key in `~/.config/opsie/backup-key.txt`, so restores here just work; save a copy somewhere else, like a password manager, because a backup you copied off the server is useless without it. You can then remove it from the server, and restores ask you to paste it. Or use the public key of an age key you already have. Making a new key keeps the old one in the file, so older backups still open.
