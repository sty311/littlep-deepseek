package bridge

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

type Config struct {
	APIKey        string `json:"deepseek_api_key"`
	APIBase       string `json:"api_base"`
	Model         string `json:"model"`
	Listen        string `json:"listen"`
	ContextBudget int    `json:"context_budget"`
	WebSearch     bool   `json:"web_search"`
	MaxSearchUses int    `json:"max_search_uses"`
	LogLevel      string `json:"log_level"`
}

var maxSearchUses = 3

func LoadConfig(path string) (Config, error) {
	c := Config{APIBase: "https://api.deepseek.com", Model: "deepseek-flash", Listen: "127.0.0.1:18181", ContextBudget: 128000, WebSearch: true, MaxSearchUses: 3, LogLevel: "info"}
	f, e := os.Open(path)
	if e != nil {
		return c, errors.New("cannot open configuration")
	}
	defer f.Close()
	d := json.NewDecoder(io.LimitReader(f, 65537))
	d.DisallowUnknownFields()
	if d.Decode(&c) != nil {
		return c, errors.New("invalid configuration JSON")
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return c, errors.New("configuration contains trailing data")
	}
	return c, c.Validate()
}
func (c Config) Validate() error {
	if strings.TrimSpace(c.APIKey) == "" || c.APIKey == "YOUR_DEEPSEEK_API_KEY" || len(c.APIKey) > 512 {
		return errors.New("set a private DeepSeek API key")
	}
	u, e := url.Parse(c.APIBase)
	if e != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("api_base must be a credential-free HTTPS base URL")
	}
	host, port, e := net.SplitHostPort(c.Listen)
	ip := net.ParseIP(host)
	if e != nil || ip == nil || !ip.IsLoopback() || port == "0" {
		return errors.New("listen must use an explicit loopback IP and nonzero port")
	}
	if c.Model == "" || c.ContextBudget < 32768 || c.ContextBudget > 1000000 || c.MaxSearchUses < 1 || c.MaxSearchUses > 3 {
		return errors.New("invalid model, context budget or search limit")
	}
	if c.LogLevel != "info" && c.LogLevel != "error" {
		return errors.New("log_level must be info or error")
	}
	return nil
}

// Run configures one server per process. Business protocol code remains the r6 baseline.
func Run(path string) error {
	c, e := LoadConfig(path)
	if e != nil {
		return e
	}
	CONTEXT_BUDGET = c.ContextBudget
	maxSearchUses = c.MaxSearchUses
	log.SetFlags(log.LstdFlags | log.LUTC)
	if c.LogLevel == "error" {
		log.SetOutput(io.Discard)
	}
	base := strings.TrimRight(c.APIBase, "/")
	client := deepSeekClient{url: base + "/chat/completions", key: c.APIKey, model: c.Model, http: &http.Client{Timeout: 60 * time.Second}}
	var provider SearchProvider
	if c.WebSearch {
		provider, e = NewDeepSeekNativeSearchProvider(base+"/anthropic/v1/messages", c.APIKey, c.Model, &http.Client{Timeout: 20 * time.Second})
		if e != nil {
			return errors.New("search configuration invalid")
		}
	}
	server := &http.Server{Addr: c.Listen, Handler: newHandler(client, provider), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 165 * time.Second, IdleTimeout: 30 * time.Second}
	log.Printf("[LITTLEP-BRIDGE] listening addr=%s version=%s", server.Addr, version)
	return server.ListenAndServe()
}
