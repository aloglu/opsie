# Security and updates

## Security

The **Security** tab gathers who can get in and what's exposed:

- **Access**: turn SSH password login off (only once you have a key, so you can't lock yourself out), allow root only with a key or not at all, and switch passwordless sudo on or off.
- **SSH keys**: every key that can log in, with the one you're using marked. Add a key by pasting it; opsie checks it's a public key and shows its fingerprint before saving. Remove keys, except the one you're logged in with.
- **Sessions**: who's logged in and from where. End any session but your own.
- **Firewall**: ufw's rules. Turn it on (SSH is allowed first), allow a port, block an address, delete a rule. The rule that lets SSH in can't be deleted.
- **Listening**: every port something listens on, and whether it's open, blocked by the firewall, or only reachable from the server itself. Ports Docker publishes are called out, because Docker bypasses ufw.
- **Logins**: the last few SSH logins, from where and with which key.
- **Failed logins**: attempts in the last day, the busiest addresses (block one with a tap), and fail2ban, which opsie can install and point at the SSH log.

SSH changes go into one file, `/etc/ssh/sshd_config.d/0-opsie.conf`, which sorts before the others so its values win. opsie checks the result with `sshd -t` before reloading, confirms with `sshd -T` that sshd uses the new values, and puts the old file back if anything fails. sudo changes are checked with `visudo` the same way.

## Updates

The **Updates** tab lists packages with a newer version, security updates first. Check for updates, install one or all of them (config files you've changed are kept, nothing is removed), and turn on automatic security updates. Below that is what's waiting for a restart: the server itself after a kernel update (restart now, or schedule it for a time you pick), and services still running a library an update replaced, each of which you can restart on its own.
