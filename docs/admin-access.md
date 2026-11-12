# How opsie gets root

opsie runs as you. Checks and actions that need root go through a small helper it starts once with `sudo -n opsie helper`, which answers a fixed list of requests (read the SSH config, list containers, restart a unit, and so on) and refuses anything else. It exits when opsie does.

If your sudo asks for a password, opsie opens with those checks marked as locked; open **Admin access** and choose **Unlock** to type your password once.

Every action is written to an audit log before it runs and again with its result (secrets like a pasted backup key are left out). **Settings → Background → Actions taken** lists them, newest first, with whether each worked; `opsie paths` shows where the file is.
