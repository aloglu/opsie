package agents

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"
)

// Keys are the API keys of the services to ask, as set in config.toml.
// An empty one isn't asked.
type Keys struct {
	OpenRouter, DeepSeek string
}

// ServiceInfo describes a service opsie can ask about spending.
type ServiceInfo struct {
	// ID is also the name of its key in config.toml, before "_key".
	ID, Name, Host string
}

// Known are the services that can be asked, in the order they're shown.
var Known = []ServiceInfo{
	{"openrouter", "OpenRouter", "openrouter.ai"},
	{"deepseek", "DeepSeek", "api.deepseek.com"},
}

// Balance is money left in an account, in one currency.
type Balance struct {
	Currency string
	// Total is what's left; Granted is the part of it that was given
	// rather than paid for.
	Total, Granted float64
}

// Service is what one pay-as-you-go service says about a key: what it
// has spent, and what's left to spend.
type Service struct {
	ID, Name string
	// Label is the key's name at the service.
	Label string
	// Spent with this key, in dollars: today, this week (from Monday) and
	// this month, by UTC, and since the key was made.
	Today, Week, Month, AllTime float64
	HaveSpend                   bool
	// Limit is the most the key may spend, and Remaining what's left of
	// it; Reset says how often it starts over ("" is never).
	Limit, Remaining float64
	HaveLimit        bool
	Reset            string
	// Bought and Used are the account's credits, when the key may see
	// them.
	Bought, Used float64
	HaveCredits  bool
	Balances     []Balance
	// FreeUsed of FreeLimit requests to free models are used today.
	FreeUsed, FreeLimit int
	// Blocked is set when the service says the key can't be used now.
	Blocked bool
	Err     string
}

// Services asks every service that has a key. Each key is sent only to
// its own service.
func Services(ctx context.Context, keys Keys) []Service {
	var out []Service
	if keys.OpenRouter != "" {
		out = append(out, openRouter(ctx, keys.OpenRouter))
	}
	if keys.DeepSeek != "" {
		out = append(out, deepSeek(ctx, keys.DeepSeek))
	}
	return out
}

var (
	openRouterURL = "https://openrouter.ai/api/v1"
	deepSeekURL   = "https://api.deepseek.com"
)

// errStatus is an answer that wasn't a 200.
type errStatus int

func (e errStatus) Error() string {
	switch int(e) {
	case 401, 403:
		return "the key was refused"
	case 429:
		return "asked too often; it'll try again"
	}
	return fmt.Sprintf("the service answered %d", int(e))
}

// ask GETs a JSON answer with a key.
func ask(ctx context.Context, url, key string, into any) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "opsie")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return errors.New("couldn't reach the service")
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 256<<10))
	if resp.StatusCode != 200 {
		return errStatus(resp.StatusCode)
	}
	if json.Unmarshal(body, into) != nil {
		return errors.New("the answer wasn't what was expected")
	}
	return nil
}

// openRouter reads GET /key, which any key may ask about itself, and GET
// /credits, the account's credits, which only a management key may see.
func openRouter(ctx context.Context, key string) Service {
	s := Service{ID: "openrouter", Name: "OpenRouter"}
	var k struct {
		Data *struct {
			Label     string   `json:"label"`
			Usage     float64  `json:"usage"`
			Daily     float64  `json:"usage_daily"`
			Weekly    float64  `json:"usage_weekly"`
			Monthly   float64  `json:"usage_monthly"`
			Limit     *float64 `json:"limit"`
			Remaining *float64 `json:"limit_remaining"`
			Reset     *string  `json:"limit_reset"`
			Free      *struct {
				Used  int `json:"used"`
				Limit int `json:"limit"`
			} `json:"free_model_daily_requests"`
		} `json:"data"`
	}
	if err := ask(ctx, openRouterURL+"/key", key, &k); err != nil {
		s.Err = err.Error()
		return s
	}
	if k.Data == nil {
		s.Err = "the answer wasn't what was expected"
		return s
	}
	d := k.Data
	s.Label = d.Label
	s.Today, s.Week, s.Month, s.AllTime, s.HaveSpend = d.Daily, d.Weekly, d.Monthly, d.Usage, true
	if d.Limit != nil {
		s.Limit, s.HaveLimit = *d.Limit, true
		s.Remaining = *d.Limit - d.Usage
		if d.Remaining != nil {
			s.Remaining = *d.Remaining
		}
		if d.Reset != nil {
			s.Reset = *d.Reset
		}
		s.Blocked = s.Remaining <= 0
	}
	if d.Free != nil {
		s.FreeUsed, s.FreeLimit = d.Free.Used, d.Free.Limit
	}
	var c struct {
		Data *struct {
			Bought float64 `json:"total_credits"`
			Used   float64 `json:"total_usage"`
		} `json:"data"`
	}
	if ask(ctx, openRouterURL+"/credits", key, &c) == nil && c.Data != nil {
		s.Bought, s.Used, s.HaveCredits = c.Data.Bought, c.Data.Used, true
	}
	return s
}

// deepSeek reads GET /user/balance: what's left, per currency.
func deepSeek(ctx context.Context, key string) Service {
	s := Service{ID: "deepseek", Name: "DeepSeek"}
	var b struct {
		Available *bool `json:"is_available"`
		Infos     []struct {
			Currency string `json:"currency"`
			Total    string `json:"total_balance"`
			Granted  string `json:"granted_balance"`
		} `json:"balance_infos"`
	}
	if err := ask(ctx, deepSeekURL+"/user/balance", key, &b); err != nil {
		s.Err = err.Error()
		return s
	}
	for _, i := range b.Infos {
		total, err := strconv.ParseFloat(i.Total, 64)
		if err != nil {
			continue
		}
		granted, _ := strconv.ParseFloat(i.Granted, 64)
		s.Balances = append(s.Balances, Balance{Currency: i.Currency, Total: total, Granted: granted})
	}
	s.Blocked = b.Available != nil && !*b.Available
	if len(s.Balances) == 0 && b.Available == nil {
		s.Err = "the answer wasn't what was expected"
	}
	return s
}
