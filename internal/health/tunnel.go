package health

import (
	"fmt"
	"strings"
	"time"

	"github.com/aloglu/opsie/internal/config"
	"github.com/aloglu/opsie/internal/facts"
	"github.com/aloglu/opsie/internal/human"
	"github.com/aloglu/opsie/internal/priv"
	"github.com/aloglu/opsie/internal/sites"
)

// tunnels checks each Cloudflare Tunnel connector: that it runs, that it's
// connected, and that the apps behind its hostnames answer.
func tunnels(f facts.Facts) []Result {
	var out []Result
	for _, t := range f.Tunnels {
		r := Result{ID: "tunnel:" + t.Name, Title: "Tunnel", Fixes: TunnelFixes(t)}
		if len(f.Tunnels) > 1 {
			r.Title = "Tunnel " + t.Name
		}
		var down []string
		for _, o := range t.Origins {
			if o.Recent > 0 {
				down = append(down, o.Host)
			}
		}
		switch {
		case !t.Running:
			r.Status, r.Summary = Crit, "stopped"
			r.Detail = []string{"cloudflared isn't running, so every site behind the tunnel is unreachable from the internet."}
		case t.HaveReady && t.Ready == 0:
			r.Status, r.Summary = Crit, "not connected"
			r.Detail = []string{"cloudflared runs but has no connection to Cloudflare, so the sites behind it are unreachable. Its log usually says why."}
		case len(down) > 0:
			r.Status = Warn
			r.Summary = down[0] + " is down"
			if len(down) > 1 {
				r.Summary = human.Plural(len(down), "site", "sites") + " down"
			}
			for _, o := range t.Origins {
				if o.Recent > 0 {
					r.Detail = append(r.Detail, fmt.Sprintf("%s: %s (%s in the last hour).", o.Host, o.Error, human.Plural(o.Recent, "failed request", "failed requests")))
				}
			}
		case !t.HaveReady:
			r.Status, r.Summary = Info, "running"
			r.Detail = []string{"cloudflared runs, but its status page didn't answer, so opsie can't tell whether it's connected."}
		default:
			r.Status, r.Summary = OK, human.Plural(t.Ready, "connection", "connections")
			if len(t.Locations) > 0 {
				r.Detail = []string{"Connected to Cloudflare in " + strings.Join(t.Locations, ", ") + "."}
			}
		}
		out = append(out, r)
	}
	return out
}

// TunnelFixes are the actions for a tunnel connector.
func TunnelFixes(t priv.Tunnel) []Fix {
	open := Fix{Label: "Open tunnel", Special: "open:docker:tunnel:" + t.Name}
	if t.Kind == "service" {
		args := map[string]string{"unit": t.Name}
		return []Fix{open, {Label: "Logs", Special: "logs", Args: args},
			{Label: "Restart", Op: "unit.restart", Args: args, Danger: true, Done: "Restarted the tunnel",
				Confirm: "Restart cloudflared? Every site behind the tunnel drops for a few seconds."}}
	}
	args := map[string]string{"container": t.Name}
	restart := Fix{Label: "Restart", Op: "docker.restart", Args: args, Danger: true, Done: "Restarted the tunnel",
		Confirm: "Restart cloudflared? Every site behind the tunnel drops for a few seconds."}
	if !t.Running {
		restart = Fix{Label: "Start", Op: "docker.start", Args: args, Done: "Started the tunnel"}
	}
	return []Fix{open, {Label: "Logs", Special: "logs", Args: args}, restart}
}

// sitesCheck reports on the public websites: one line for each that's
// down or slow, so a second site failing alerts too, or one line saying
// they're all fine.
func sitesCheck(f facts.Facts, cfg config.Sites) []Result {
	if len(f.Sites) == 0 {
		return nil
	}
	slow := time.Duration(cfg.SlowSeconds * float64(time.Second))
	if slow <= 0 {
		slow = 5 * time.Second
	}
	var out []Result
	for _, s := range f.Sites {
		r := Result{ID: "site:" + s.URL, Title: s.Host, Fixes: siteFixes(s)}
		days := 0
		if !s.CertExpires.IsZero() {
			days = int(time.Until(s.CertExpires).Hours() / 24)
		}
		switch {
		case !s.Up():
			r.Status, r.Summary = Crit, "down"
			r.Detail = []string{s.URL + " doesn't work for visitors: " + s.Problem() + "."}
		case !s.CertExpires.IsZero() && days < 14:
			r.Status, r.Summary = Warn, "certificate expires in "+human.Plural(max(days, 0), "day", "days")
			r.Detail = []string{"Visitors get a warning once it runs out. Cloudflare renews its own certificates, so this is usually a site that isn't behind it."}
		case s.Took > slow:
			r.Status, r.Summary = Warn, "slow · "+human.Seconds(s.Took)
			r.Detail = []string{fmt.Sprintf("It took %s to answer; more than %s counts as slow.", human.Seconds(s.Took), human.Seconds(slow))}
		default:
			continue
		}
		out = append(out, r)
	}
	if len(out) == 0 {
		r := Result{ID: "sites", Status: OK, Title: "Sites", Summary: human.Plural(len(f.Sites), "site answers", "sites answer")}
		if len(f.Sites) == 1 {
			r.Summary = "1 site answers"
		}
		for _, s := range f.Sites {
			r.Detail = append(r.Detail, fmt.Sprintf("%s: %d in %s.", s.Host, s.Status, human.Seconds(s.Took)))
		}
		out = append(out, r)
	}
	return out
}

func siteFixes(s sites.Result) []Fix {
	if s.Route {
		return []Fix{{Label: "Open in Docker", Special: "open:docker:route:" + strings.TrimPrefix(s.URL, "https://")}}
	}
	return []Fix{{Label: "Open in Docker", Special: "open:docker:site:" + s.URL}}
}

// images says which containers have a newer image waiting. It's news, not
// a problem, so it never alerts.
func images(f facts.Facts) []Result {
	var newer []string
	seen := map[string]bool{}
	for _, c := range f.ImageChecks {
		if c.Newer && !seen[c.Ref] {
			seen[c.Ref] = true
			newer = append(newer, c.Ref)
		}
	}
	if len(newer) == 0 {
		return nil
	}
	return []Result{{ID: "images", Status: Info, Title: "New images", Summary: human.Plural(len(newer), "image", "images"),
		Detail: []string{"Newer versions are out for " + strings.Join(newer, ", ") + ". Pull & recreate on the app's page installs one; the app restarts for a moment."},
		Fixes:  []Fix{{Label: "Open Docker tab", Special: "tab:docker"}}}}
}
