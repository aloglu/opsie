package tui

import (
	"fmt"
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/aloglu/opsie/internal/daemon"
	"github.com/aloglu/opsie/internal/health"
	"github.com/aloglu/opsie/internal/human"
	"github.com/aloglu/opsie/internal/priv"
)

// --- page ---

func (m *Model) securitySections(inner int) []section {
	t := m.th
	f := m.facts
	if !m.haveFacts {
		return []section{{title: "Security", intro: []string{t.dim.Render("Checking…")}}}
	}
	if !f.Admin.Available {
		return []section{{title: "Security", intro: wrap("SSH, firewall and sudo settings can only be read with admin access, which is locked.", inner),
			entries: []entry{{id: "admin", lines: m.action("Unlock admin access", inner)}}}}
	}
	return []section{m.accessSection(inner), m.keysSection(inner), m.sessionsSection(inner), m.loginsSection(inner),
		m.firewallSection(inner), m.portsSection(inner), m.attemptsSection(inner)}
}

func (m *Model) accessSection(inner int) section {
	t := m.th
	f := m.facts
	s := section{title: "Access"}
	if f.SSHDErr != "" {
		s.intro = wrap("SSH settings: "+f.SSHDErr, inner)
	} else {
		pw := t.statusIcon(health.OK) + " Password login"
		pwState := t.okText.Render("off")
		if health.PasswordsOn(f) {
			pw, pwState = t.statusIcon(health.Warn)+" Password login", t.warnText.Render("on")
		}
		s.entries = append(s.entries, entry{id: "ssh:passwords", lines: m.itemRow(pw, pwState, inner)})
		root := f.SSHD["permitrootlogin"]
		icon, style := t.statusIcon(health.OK), t.okText
		if root == "yes" {
			icon, style = t.statusIcon(health.Warn), t.warnText
		}
		s.entries = append(s.entries, entry{id: "ssh:root", lines: m.itemRow(icon+" Root login", style.Render(rootWord(root)), inner)})
	}
	icon, state := t.statusIcon(health.OK), t.okText.Render("asks for password")
	if f.PasswordlessSudo {
		icon, state = t.statusIcon(health.Info), t.subtle.Render("no password")
	}
	s.entries = append(s.entries, entry{id: "sudo", lines: m.itemRow(icon+" sudo", state, inner)})
	return s
}

func rootWord(v string) string {
	switch v {
	case "prohibit-password", "without-password":
		return "keys only"
	case "no":
		return "off"
	case "yes":
		return "with password"
	case "forced-commands-only":
		return "commands only"
	}
	return orDash(v)
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

func (m *Model) keysSection(inner int) section {
	t := m.th
	f := m.facts
	total := 0
	for _, k := range f.Keys {
		total += len(k.Keys)
	}
	s := section{title: "SSH keys", badge: t.subtle.Render(strconv.Itoa(total))}
	if f.KeysErr != "" {
		s.intro = wrap(f.KeysErr, inner)
		return s
	}
	for _, set := range f.Keys {
		for _, k := range set.Keys {
			left := t.bold.Render(set.User) + " " + t.subtle.Render(shortType(k.Type))
			if k.Comment != "" {
				left += " " + k.Comment
			}
			right := t.dim.Render(shortPrint(k.Fingerprint))
			if k.Fingerprint == f.SessionKey {
				right = t.infoText.Render("this session")
			}
			s.entries = append(s.entries, entry{id: "key:" + set.User + ":" + k.Fingerprint, lines: m.itemRow("◆ "+left, right, inner)})
		}
	}
	if total == 0 {
		s.intro = wrap("No keys can log in, so SSH relies on passwords.", inner)
	}
	if f.User != "" {
		s.entries = append(s.entries, entry{id: "do:keyadd:" + f.User, lines: m.action("Add a key for "+f.User, inner)})
	}
	return s
}

func shortType(t string) string {
	t = strings.TrimPrefix(t, "ssh-")
	t = strings.TrimPrefix(t, "sk-ssh-")
	if strings.HasPrefix(t, "ecdsa") {
		return "ecdsa"
	}
	return strings.TrimSuffix(t, "@openssh.com")
}

// shortPrint is the tail of a fingerprint, enough to tell keys apart.
func shortPrint(fp string) string {
	fp = strings.TrimPrefix(fp, "SHA256:")
	if len(fp) > 8 {
		return "…" + fp[len(fp)-8:]
	}
	return fp
}

func (m *Model) sessionsSection(inner int) section {
	t := m.th
	s := section{title: "Sessions", badge: t.subtle.Render(strconv.Itoa(len(m.snap.Sessions)))}
	if len(m.snap.Sessions) == 0 {
		s.intro = []string{t.dim.Render("Nobody's logged in.")}
	}
	for _, ss := range m.snap.Sessions {
		left := t.bold.Render(ss.User) + " " + t.subtle.Render(orDash(ss.From))
		right := t.dim.Render(human.Duration(m.now.Sub(ss.Started)))
		if ss.ID != "" && ss.ID == m.facts.Session.ID {
			right = t.infoText.Render("you") + t.dim.Render(" · ") + right
		}
		if ss.ID == "" {
			s.intro = append(s.intro, m.itemRow("● "+left, right, inner)...)
			continue
		}
		s.entries = append(s.entries, entry{id: "sess:" + ss.ID, lines: m.itemRow("● "+left, right, inner)})
	}
	return s
}

// loginsSection lists the last successful SSH logins and the key each
// used.
func (m *Model) loginsSection(inner int) section {
	t := m.th
	f := m.facts
	s := section{title: "Recent logins"}
	if f.FailuresErr != "" {
		s.intro = []string{t.dim.Render("Couldn't read the SSH log.")}
		return s
	}
	list := f.Failures.Accepted
	if len(list) == 0 {
		s.intro = []string{t.dim.Render("No SSH logins in the last day.")}
	}
	for i := len(list) - 1; i >= 0 && i >= len(list)-5; i-- {
		l := list[i]
		left := t.bold.Render(l.User) + " " + t.subtle.Render(l.From)
		if k := daemon.KeyName(f, l); k != "" && inner >= 50 {
			left += t.dim.Render(" · " + k)
		}
		s.intro = append(s.intro, m.itemRow(left, t.dim.Render(human.Duration(m.now.Sub(l.At))+" ago"), inner)...)
	}
	alerts := map[string]string{"new": "Background checks alert you when someone logs in from an address they haven't seen.",
		"all": "Background checks alert you about every login.", "off": "Login alerts are off."}[m.env.Config.Notify.Logins]
	s.outro = wrap(t.dim.Render(alerts), inner)
	return s
}

func (m *Model) firewallSection(inner int) section {
	t := m.th
	f := m.facts
	r := f.Rules
	s := section{title: "Firewall"}
	switch {
	case f.RulesErr != "":
		s.intro = wrap(f.RulesErr, inner)
		return s
	case !r.Installed && f.Firewall.Tool == "nftables":
		s.badge = t.subtle.Render("nftables")
		s.intro = wrap("This server uses nftables directly; opsie only manages ufw, so change its rules by hand.", inner)
		return s
	case !r.Installed:
		s.badge = t.warnText.Render("none")
		s.entries = append(s.entries, entry{id: "fw", lines: m.itemRow(t.statusIcon(health.Warn)+" ufw", t.warnText.Render("not installed"), inner)})
		return s
	case !r.Active:
		s.badge = t.warnText.Render("off")
		s.entries = append(s.entries, entry{id: "fw", lines: m.itemRow(t.statusIcon(health.Warn)+" ufw", t.warnText.Render("off"), inner)})
		return s
	}
	s.badge = t.okText.Render("on")
	s.entries = append(s.entries, entry{id: "fw", lines: m.itemRow(t.statusIcon(health.OK)+" ufw", t.subtle.Render(strings.TrimSuffix(firstPolicy(r.Default), " (incoming)")+" incoming"), inner)})
	for _, rule := range r.Rules {
		style := t.okText
		if !strings.HasPrefix(rule.Action, "ALLOW") && !strings.HasPrefix(rule.Action, "LIMIT") {
			style = t.dangerText
		}
		left := "  " + t.bold.Render(rule.To)
		right := style.Render(strings.ToLower(strings.TrimSuffix(rule.Action, " IN"))) + t.subtle.Render(" from "+rule.From)
		s.entries = append(s.entries, entry{id: "rule:" + rule.Text(), lines: m.itemRow(left, right, inner)})
	}
	s.entries = append(s.entries,
		entry{id: "do:fw:allow", lines: m.action("Allow a port", inner)},
		entry{id: "do:fw:deny", lines: m.action("Block an address", inner)})
	return s
}

// firstPolicy is ufw's incoming policy from its default line.
func firstPolicy(d string) string {
	first, _, _ := strings.Cut(d, ",")
	return strings.TrimSpace(first)
}

// exposure says who can reach a listener.
func (m *Model) exposure(l priv.Listener) (string, health.Status) {
	r := m.facts.Rules
	switch {
	case !l.Public:
		return "this server only", health.OK
	case l.Docker:
		return "open · via Docker", health.Info
	case !r.Active:
		return "open", health.Info
	case priv.AllowsPort(r.Rules, l.Port, l.Proto):
		return "open", health.OK
	}
	return "blocked", health.OK
}

func (m *Model) portsSection(inner int) section {
	t := m.th
	f := m.facts
	s := section{title: "Listening"}
	if f.ListenErr != "" {
		s.intro = wrap(f.ListenErr, inner)
		return s
	}
	open := 0
	for _, l := range f.Listen {
		word, st := m.exposure(l)
		if strings.HasPrefix(word, "open") {
			open++
		}
		style := t.subtle
		switch {
		case strings.HasPrefix(word, "open") && st == health.Info:
			style = t.warnText
		case strings.HasPrefix(word, "open"):
			style = t.okText
		}
		left := t.bold.Render(fmt.Sprintf("%d", l.Port)) + t.subtle.Render("/"+l.Proto) + " " + orDash(l.Process)
		s.entries = append(s.entries, entry{id: fmt.Sprintf("port:%s:%d:%s", l.Proto, l.Port, l.Process), lines: m.itemRow(left, style.Render(word), inner)})
	}
	s.badge = t.subtle.Render(fmt.Sprintf("%d open", open))
	return s
}

func (m *Model) attemptsSection(inner int) section {
	t := m.th
	f := m.facts
	s := section{title: "Failed logins"}
	if f.FailuresErr != "" {
		s.intro = wrap(f.FailuresErr, inner)
		return s
	}
	fl := f.Failures
	s.badge = t.subtle.Render(fmt.Sprintf("%d today", fl.Total))
	if fl.Total == 0 {
		s.intro = []string{t.dim.Render("None in the last day.")}
	} else if len(fl.Users) > 0 {
		s.intro = wrap(t.subtle.Render("Tried: ")+strings.Join(fl.Users, ", "), inner)
	}
	for _, c := range fl.Top {
		right := t.subtle.Render(fmt.Sprintf("%d× · %s ago", c.Count, human.Duration(m.now.Sub(c.Last))))
		s.entries = append(s.entries, entry{id: "ip:" + c.IP, lines: m.itemRow("  "+c.IP, right, inner)})
	}
	f2b := f.Fail2ban
	word, style := "not installed", t.subtle
	switch {
	case f2b.Running:
		banned := 0
		for _, j := range f2b.Jails {
			banned += len(j.Banned)
		}
		word, style = fmt.Sprintf("on · %d banned", banned), t.okText
	case f2b.Installed:
		word, style = "stopped", t.warnText
	}
	if f.Fail2banErr == "" {
		s.entries = append(s.entries, entry{id: "f2b", lines: m.itemRow("fail2ban", style.Render(word), inner)})
	}
	return s
}

// --- details ---

func (m *Model) securityDetail(key string, b *detailBuilder) (detail, bool) {
	t := m.th
	f := m.facts
	plain := lipgloss.NewStyle()
	kind, arg, _ := strings.Cut(key, ":")
	switch kind {
	case "ssh":
		if arg == "passwords" {
			on := health.PasswordsOn(f)
			st, word := health.OK, "off"
			if on {
				st, word = health.Warn, "on"
				b.text("Anyone on the internet can try passwords over SSH, and bots do, all day. With an SSH key you don't need them.", plain)
				if health.MyKeys(f) == 0 {
					b.text("You don't have a key yet, so turning passwords off would lock you out. Add one in SSH keys first.", t.warnText)
				}
			} else {
				b.text("Only SSH keys can log in. Password guessing can't get in, however long it goes on.", plain)
			}
			b.kv("Password", f.SSHD["passwordauthentication"])
			b.kv("Keyboard", f.SSHD["kbdinteractiveauthentication"])
			b.kv("Your keys", strconv.Itoa(health.MyKeys(f)))
			b.text("opsie writes its SSH settings to /etc/ssh/sshd_config.d/0-opsie.conf, checks them with sshd -t before reloading, and puts the old file back if anything fails.", t.subtle)
			return detail{title: "Password login", headline: headline(t, st, word, ""), body: b.body, fixes: health.PasswordFixes(f)}, true
		}
		v := f.SSHD["permitrootlogin"]
		st := health.OK
		if v == "yes" {
			st = health.Warn
		}
		b.text(map[string]string{
			"yes":                  "Root can log in with a password. Root is the first username every bot tries.",
			"prohibit-password":    "Root can log in, but only with a key.",
			"without-password":     "Root can log in, but only with a key.",
			"no":                   "Nobody can log in as root over SSH. Log in as yourself and use sudo.",
			"forced-commands-only": "Root can only log in with a key that's limited to a set command.",
		}[v], plain)
		return detail{title: "Root login", headline: headline(t, st, rootWord(v), ""), body: b.body, fixes: health.RootFixes(f)}, true

	case "sudo":
		st, word := health.OK, "asks for a password"
		if f.PasswordlessSudo {
			st, word = health.Info, "no password"
			b.text(f.User+" can run anything as root without a password. That's convenient, but anyone who gets into this account gets root too.", plain)
			if !f.Sudo.HasPassword {
				b.text(f.User+" has no password, so sudo couldn't ask for one. Set one with passwd first.", t.warnText)
			}
		} else {
			b.text("sudo asks for "+f.User+"'s password, so a stolen SSH key alone doesn't give root. opsie asks for it once when it starts.", plain)
		}
		for _, src := range f.Sudo.Sources {
			b.kv("Allowed by", src)
		}
		if f.Sudo.Override {
			b.kv("Override", "/etc/sudoers.d/99-zz-opsie-"+f.User)
		}
		return detail{title: "sudo", headline: headline(t, st, word, ""), body: b.body, fixes: health.SudoFixes(f)}, true

	case "key":
		user, fp, _ := strings.Cut(arg, ":")
		for _, set := range f.Keys {
			if set.User != user {
				continue
			}
			for _, k := range set.Keys {
				if k.Fingerprint != fp {
					continue
				}
				b.kv("User", user)
				b.kv("Type", k.Type)
				b.kv("Comment", orDash(k.Comment))
				b.kv("Print", k.Fingerprint)
				b.kv("File", set.File)
				var fixes []health.Fix
				switch {
				case fp == f.SessionKey:
					b.text("You're logged in with this key, so it can't be removed from here.", t.subtle)
				case k.Options:
					b.text("This key has options in front of it; edit "+set.File+" to change it.", t.subtle)
				default:
					fixes = []health.Fix{{
						Label: "Remove key", Op: "ssh.key-remove", Danger: true,
						Args:    map[string]string{"user": user, "fingerprint": fp, "session_key": f.SessionKey},
						Confirm: "Whoever has this key won't be able to log in as " + user + " anymore.",
						Done:    "Key removed",
					}}
				}
				return detail{title: "SSH key", headline: headline(t, health.Info, shortType(k.Type), k.Comment), body: b.body, fixes: fixes}, true
			}
		}

	case "do":
		sub, rest, _ := strings.Cut(arg, ":")
		switch sub {
		case "keyadd":
			b.text("Paste a public key: one line starting with ssh-ed25519, ssh-rsa or ecdsa-sha2-, from a .pub file. On your computer, `cat ~/.ssh/id_ed25519.pub` prints it.", plain)
			b.text("Termius: Keychain › your key › Export to host, or copy its public key.", t.subtle)
			return detail{title: "Add an SSH key", body: b.body, fixes: []health.Fix{{
				Label: "Add key", Op: "ssh.key-add", Args: map[string]string{"user": rest}, Done: "Key added",
				Input: &health.Input{Prompt: "Public key for " + rest, Arg: "key", Placeholder: "ssh-ed25519 AAAA…", Kind: "key"},
			}}}, true
		case "fw":
			if rest == "allow" {
				b.text("Let connections in on a port, from anywhere. Leave off the protocol to allow both tcp and udp.", plain)
				return detail{title: "Allow a port", body: b.body, fixes: []health.Fix{{
					Label: "Allow", Op: "ufw.allow", Done: "Allowed",
					Input: &health.Input{Prompt: "Port", Arg: "port", Placeholder: "443/tcp", Kind: "port"},
				}}}, true
			}
			b.text("Block every connection from an address or range, ahead of any other rule.", plain)
			if f.Session.IP != "" {
				b.kv("You're at", f.Session.IP)
			}
			return detail{title: "Block an address", body: b.body, fixes: []health.Fix{{
				Label: "Block", Op: "ufw.deny", Args: map[string]string{"caller_ip": f.Session.IP}, Done: "Blocked",
				Input: &health.Input{Prompt: "Address", Arg: "ip", Placeholder: "203.0.113.7", Kind: "ip"},
			}}}, true
		}

	case "sess":
		for _, ss := range m.snap.Sessions {
			if ss.ID != arg {
				continue
			}
			b.kv("User", ss.User)
			b.kv("From", orDash(ss.From))
			b.kv("Terminal", orDash(ss.Terminal))
			b.kv("Since", ss.Started.Local().Format("Mon 2 Jan 15:04")+" · "+human.Duration(m.now.Sub(ss.Started)))
			var fixes []health.Fix
			if arg == f.Session.ID {
				b.text("This is the session you're using.", t.subtle)
			} else {
				fixes = []health.Fix{{
					Label: "End session", Op: "session.terminate", Danger: true,
					Args:    map[string]string{"session": arg, "caller_session": f.Session.ID},
					Confirm: "Disconnect " + ss.User + " and stop everything running in that session?",
					Done:    "Session ended",
				}}
			}
			return detail{title: "Session " + arg, headline: headline(t, health.Info, ss.User, orDash(ss.From)), body: b.body, fixes: fixes}, true
		}

	case "fw":
		r := f.Rules
		st, word := health.OK, "on"
		switch {
		case !r.Installed:
			st, word = health.Warn, "not installed"
			b.text("There's no firewall, so every listening port can be reached from the internet unless your provider filters traffic.", plain)
		case !r.Active:
			st, word = health.Warn, "off"
			b.text("ufw is installed but off, so every listening port can be reached from the internet.", plain)
		default:
			b.text("ufw blocks connections that no rule allows.", plain)
			b.kv("Policy", r.Default)
			b.kv("Rules", strconv.Itoa(len(r.Rules)))
		}
		b.text("Docker publishes ports with its own firewall rules, ahead of ufw's, so ufw doesn't filter container ports.", t.subtle)
		return detail{title: "Firewall", headline: headline(t, st, word, ""), body: b.body, fixes: health.FirewallFixes(f)}, true

	case "rule":
		for _, rule := range f.Rules.Rules {
			if rule.Text() != arg {
				continue
			}
			b.kv("Port", rule.To)
			b.kv("Action", strings.ToLower(rule.Action))
			b.kv("From", rule.From)
			b.text("opsie won't delete the rule that lets SSH in, so you can't lock yourself out.", t.subtle)
			return detail{title: "Firewall rule", headline: headline(t, health.Info, strings.ToLower(rule.Action), rule.To), body: b.body, fixes: []health.Fix{{
				Label: "Delete rule", Op: "ufw.delete", Danger: true, Args: map[string]string{"rule": rule.Text()},
				Confirm: "Delete the rule \"" + rule.Text() + "\"?", Done: "Rule deleted",
			}}}, true
		}

	case "port":
		for _, l := range f.Listen {
			if fmt.Sprintf("%s:%d:%s", l.Proto, l.Port, l.Process) != arg {
				continue
			}
			word, st := m.exposure(l)
			b.kv("Program", orDash(l.Process))
			b.kv("Port", fmt.Sprintf("%d/%s", l.Port, l.Proto))
			b.kv("Address", strings.Join(l.Addrs, ", "))
			var fixes []health.Fix
			switch {
			case !l.Public:
				b.text("It only listens on this server's own addresses, so nothing outside can reach it.", plain)
			case l.Docker:
				b.text("Docker publishes this port with its own firewall rules, so ufw doesn't filter it: it's open to the internet. To keep it private, publish it on 127.0.0.1 in the compose file (\"127.0.0.1:8080:80\").", plain)
			case word == "blocked":
				b.text("It listens on every address, but the firewall blocks it, so it can't be reached from outside.", plain)
				fixes = []health.Fix{{Label: "Allow through firewall", Op: "ufw.allow", Args: map[string]string{"port": strconv.Itoa(l.Port), "proto": l.Proto},
					Done: fmt.Sprintf("Port %d/%s is open", l.Port, l.Proto)}}
			default:
				b.text("It can be reached from the internet.", plain)
			}
			return detail{title: fmt.Sprintf("Port %d", l.Port), headline: headline(t, st, word, ""), body: b.body, fixes: fixes}, true
		}

	case "ip":
		for _, c := range f.Failures.Top {
			if c.IP != arg {
				continue
			}
			b.kv("Attempts", fmt.Sprintf("%d in the last day", c.Count))
			b.kv("Last", human.Duration(m.now.Sub(c.Last))+" ago")
			fixes := health.BlockFix(f, c.IP)
			switch {
			case c.IP == f.Session.IP:
				b.text("That's the address you're connected from.", t.warnText)
			case !f.Rules.Active:
				b.text("Turn on the firewall to block addresses.", t.subtle)
			default:
				b.text("Blocking adds a firewall rule that drops everything from this address. Bots switch addresses often, so fail2ban, which does this automatically, works better over time.", t.subtle)
			}
			return detail{title: c.IP, headline: headline(t, health.Info, "failed logins", ""), body: b.body, fixes: fixes}, true
		}

	case "f2b":
		f2b := f.Fail2ban
		st, word := health.Info, "not installed"
		switch {
		case f2b.Running:
			st, word = health.OK, "running"
			b.text("fail2ban bans addresses that keep failing to log in, for a while, through the firewall.", plain)
			for _, j := range f2b.Jails {
				b.kv(j.Name, fmt.Sprintf("%d banned now, %d in total", len(j.Banned), j.Total))
			}
		case f2b.Installed:
			st, word = health.Warn, "stopped"
			b.text("fail2ban is installed but not running.", plain)
		default:
			b.text("fail2ban watches the SSH log and bans addresses that keep failing to log in. With password login off it mostly keeps the log quiet; with passwords on, it's what stops guessing.", plain)
		}
		return detail{title: "fail2ban", headline: headline(t, st, word, ""), body: b.body, fixes: health.Fail2banFixes(f)}, true
	}
	return detail{}, false
}
