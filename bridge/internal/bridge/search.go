package bridge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// SearchProvider supplies a short summary and structured web sources.
type SearchProvider interface {
	Search(context.Context, string) (*SearchResult, error)
}

type SearchResult struct {
	Summary string
	Sources []SearchSource
}

type SearchSource struct {
	URL     string
	Title   string
	Snippet string
	PageAge string
}

type searchTextBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type searchMessage struct {
	Role    string            `json:"role"`
	Content []searchTextBlock `json:"content"`
}

type searchTool struct {
	Type    string `json:"type"`
	Name    string `json:"name"`
	MaxUses int    `json:"max_uses"`
}

type searchRequest struct {
	Model     string          `json:"model"`
	MaxTokens int             `json:"max_tokens"`
	Messages  []searchMessage `json:"messages"`
	Tools     []searchTool    `json:"tools"`
}

type cachedSearch struct {
	result  SearchResult
	expires time.Time
}

// DeepSeekNativeSearchProvider uses DeepSeek's Anthropic-compatible server tool.
type DeepSeekNativeSearchProvider struct {
	url   string
	key   string
	model string
	http  *http.Client

	mu    sync.Mutex
	cache map[string]cachedSearch
}

// NewDeepSeekNativeSearchProvider accepts either the Messages URL or its
// /anthropic/v1 base URL. Blank URL and model use the official defaults.
func NewDeepSeekNativeSearchProvider(apiURL, key, model string, httpClient *http.Client) (*DeepSeekNativeSearchProvider, error) {
	if apiURL == "" {
		apiURL = "https://api.deepseek.com/anthropic/v1/messages"
	}
	parsed, err := url.Parse(apiURL)
	if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Hostname() == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("invalid DeepSeek search API URL")
	}
	apiURL = strings.TrimRight(apiURL, "/")
	if !strings.HasSuffix(apiURL, "/messages") {
		apiURL += "/messages"
	}
	if strings.TrimSpace(key) == "" {
		return nil, errors.New("DeepSeek search API key is missing")
	}
	if model == "" {
		model = "deepseek-flash"
	}
	if httpClient == nil {
		httpClient = &http.Client{}
	}
	return &DeepSeekNativeSearchProvider{url: apiURL, key: key, model: model, http: httpClient, cache: make(map[string]cachedSearch)}, nil
}

func normalizedSearchQuery(query string) string {
	return strings.Join(strings.Fields(query), " ")
}

func cloneSearchResult(result SearchResult) *SearchResult {
	copy := result
	copy.Sources = append([]SearchSource(nil), result.Sources...)
	return &copy
}

func (p *DeepSeekNativeSearchProvider) Search(ctx context.Context, query string) (*SearchResult, error) {
	if ctx == nil {
		return nil, errors.New("search context is missing")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	query = normalizedSearchQuery(query)
	if query == "" {
		return nil, errors.New("search query is empty")
	}
	cacheKey := strings.ToLower(query)
	p.mu.Lock()
	entry, found := p.cache[cacheKey]
	if found && !time.Now().Before(entry.expires) {
		delete(p.cache, cacheKey)
		found = false
	}
	p.mu.Unlock()
	if found {
		return cloneSearchResult(entry.result), nil
	}

	requestBody, err := json.Marshal(searchRequest{
		Model: p.model, MaxTokens: 4096,
		Messages: []searchMessage{{Role: "user", Content: []searchTextBlock{{Type: "text", Text: "Perform a web search for the query: " + query}}}},
		Tools:    []searchTool{{Type: "web_search_20250305", Name: "web_search", MaxUses: maxSearchUses}},
	})
	if err != nil {
		return nil, errors.New("cannot encode search request")
	}
	requestCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(requestCtx, http.MethodPost, p.url, bytes.NewReader(requestBody))
	if err != nil {
		return nil, errors.New("cannot build search request")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("x-api-key", p.key)
	req.Header.Set("anthropic-version", "2023-06-01")
	resp, err := p.http.Do(req)
	if err != nil {
		if requestCtx.Err() != nil {
			return nil, requestCtx.Err()
		}
		return nil, errors.New("DeepSeek search request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("DeepSeek search HTTP %d", resp.StatusCode)
	}
	// Limit the response independently of upstream Content-Length.
	data, err := io.ReadAll(io.LimitReader(resp.Body, 2*1024*1024+1))
	if err != nil {
		if requestCtx.Err() != nil {
			return nil, requestCtx.Err()
		}
		return nil, errors.New("cannot read DeepSeek search response")
	}
	if len(data) > 2*1024*1024 {
		return nil, errors.New("DeepSeek search response is too large")
	}
	result, err := parseDeepSeekSearchResponse(data)
	if err != nil {
		return nil, err
	}
	if err := requestCtx.Err(); err != nil {
		return nil, err
	}
	p.mu.Lock()
	now := time.Now()
	for key, old := range p.cache {
		if !now.Before(old.expires) {
			delete(p.cache, key)
		}
	}
	p.cache[cacheKey] = cachedSearch{result: *cloneSearchResult(*result), expires: now.Add(5 * time.Minute)}
	p.mu.Unlock()
	return result, nil
}

func validSearchURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Hostname() != "" && u.User == nil
}

func parseDeepSeekSearchResponse(data []byte) (*SearchResult, error) {
	var wire struct {
		Content []struct {
			Type    string `json:"type"`
			Text    string `json:"text"`
			Content []struct {
				Type    string `json:"type"`
				URL     string `json:"url"`
				Title   string `json:"title"`
				PageAge string `json:"page_age"`
			} `json:"content"`
			Citations []struct {
				URL       string `json:"url"`
				CitedText string `json:"cited_text"`
			} `json:"citations"`
		} `json:"content"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return nil, errors.New("invalid DeepSeek search response")
	}
	result := &SearchResult{}
	seen := make(map[string]int)
	var snippets = make(map[string]string)
	var texts []string
	for _, block := range wire.Content {
		if block.Type == "text" {
			texts = append(texts, block.Text)
			for _, citation := range block.Citations {
				if citation.CitedText != "" && snippets[citation.URL] == "" {
					snippets[citation.URL] = citation.CitedText
				}
			}
		}
		if block.Type != "web_search_tool_result" {
			continue
		}
		for _, item := range block.Content {
			if item.Type != "web_search_result" || !validSearchURL(item.URL) {
				continue
			}
			if _, exists := seen[item.URL]; exists {
				continue
			}
			if len(result.Sources) == 8 {
				continue
			}
			seen[item.URL] = len(result.Sources)
			result.Sources = append(result.Sources, SearchSource{URL: item.URL, Title: item.Title, PageAge: item.PageAge})
		}
	}
	if len(result.Sources) == 0 {
		return nil, errors.New("DeepSeek search returned no structured sources")
	}
	for i := range result.Sources {
		result.Sources[i].Snippet = snippets[result.Sources[i].URL]
	}
	result.Summary = strings.Join(texts, "\n")
	return result, nil
}
