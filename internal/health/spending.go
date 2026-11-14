package health

import (
	"fmt"

	"github.com/aloglu/opsie/internal/agents"
	"github.com/aloglu/opsie/internal/human"
)

// Spending is what the services paid by use last said about their keys,
// and how little may be left before it counts as low.
type Spending struct {
	Services []agents.Service
	// LowPercent is for a key's limit and an account's credits;
	// LowBalance for an account that only has a balance. Zero is never.
	LowPercent, LowBalance float64
}

// KeyTitle names one key of a service, telling it from the service's
// other keys when there are any.
func KeyTitle(s agents.Service, all []agents.Service) string {
	title := s.Name + " key"
	others := 0
	for _, o := range all {
		if o.ID == s.ID {
			others++
		}
	}
	switch {
	case s.KeyName != "":
		return title + " " + s.KeyName
	case others > 1 && s.Tail != "":
		return title + " ••••" + s.Tail
	}
	return title
}

func dollars(v float64) string { return fmt.Sprintf("$%.2f", v) }

// spending says which keys have run out or are about to. Keys that are
// fine share one line.
func spending(in Spending) []Result {
	if len(in.Services) == 0 {
		return nil
	}
	var out []Result
	fine, unasked := 0, 0
	for _, s := range in.Services {
		r := Result{ID: "spend:" + s.ID + ":" + s.Tail, Title: KeyTitle(s, in.Services),
			Fixes: []Fix{{Label: "Open in Agents", Special: "open:agents:service:" + s.Ref()}}}
		low := func(left, whole float64) bool {
			return in.LowPercent > 0 && whole > 0 && left/whole*100 < in.LowPercent
		}
		credits := s.Bought - s.Used
		switch {
		case s.Refused():
			r.Status, r.Summary = Warn, "refused"
			r.Detail = []string{s.Name + " refused this key. It may have been deleted or turned off there; what it has spent can't be shown until it's replaced."}
		case s.Err != "":
			unasked++
			continue
		case s.Blocked || (s.HaveCredits && s.Bought > 0 && credits <= 0):
			r.Status, r.Summary = Crit, "nothing left"
			r.Detail = []string{"Requests with this key are refused until there's more to spend: the key's limit is used up, or the account is out of credits."}
		case s.HaveLimit && low(s.Remaining, s.Limit):
			r.Status, r.Summary = Warn, dollars(s.Remaining)+" left of "+dollars(s.Limit)
			r.Detail = []string{fmt.Sprintf("Less than %s%% of this key's spending limit is left. Once it's used up, requests with the key are refused.", trimPct(in.LowPercent))}
			if s.Reset != "" {
				r.Detail = append(r.Detail, "The limit starts over "+s.Reset+".")
			}
		case s.HaveCredits && low(credits, s.Bought):
			r.Status, r.Summary = Warn, dollars(credits)+" of credits left"
			r.Detail = []string{fmt.Sprintf("Less than %s%% of the account's credits is left: %s of %s. Once they're gone, requests are refused.", trimPct(in.LowPercent), dollars(credits), dollars(s.Bought))}
		case lowBalance(s, in.LowBalance):
			r.Status, r.Summary = Warn, "balance is low"
			r.Detail = []string{fmt.Sprintf("The account's balance is under %s. Once it's gone, requests are refused.", trimPct(in.LowBalance))}
		default:
			fine++
			continue
		}
		out = append(out, r)
	}
	if len(out) == 0 && fine > 0 {
		r := Result{ID: "spend", Status: OK, Title: "AI spending", Summary: human.Plural(fine, "key", "keys") + " with enough left",
			Detail: []string{"No key of a service paid by use has run out or is about to. The Agents tab has what each has spent."},
			Fixes:  []Fix{{Label: "Open Agents", Special: "open:agents:"}}}
		if unasked > 0 {
			r.Summary += " · " + human.Plural(unasked, "wasn't", "weren't") + " reached"
		}
		out = append(out, r)
	}
	return out
}

// lowBalance reports whether every balance an account has is under low.
func lowBalance(s agents.Service, low float64) bool {
	if low <= 0 || len(s.Balances) == 0 {
		return false
	}
	for _, b := range s.Balances {
		if b.Total >= low {
			return false
		}
	}
	return true
}

func trimPct(v float64) string { return fmt.Sprintf("%g", v) }
