package inference

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// CachedClient wraps a Client with a disk-backed response cache keyed on
// (model, system prompt, messages, options). Cache hits return the stored
// response with zero usage cost. Streaming calls bypass the cache.
type CachedClient struct {
	inner Client
	dir   string
	mu    sync.Mutex
}

// NewCachedClient returns a caching wrapper around inner. Cache files are
// stored in dir, which is created if it doesn't exist.
func NewCachedClient(inner Client, dir string) *CachedClient {
	return &CachedClient{inner: inner, dir: dir}
}

type cachedEntry struct {
	Text         string `json:"text"`
	InputTokens  int    `json:"input_tokens"`
	OutputTokens int    `json:"output_tokens"`
	Cost         float64 `json:"cost"`
	Error        string `json:"error,omitempty"`
	Truncated    bool   `json:"truncated,omitempty"`
}

func (c *CachedClient) cacheKey(system string, messages []Message, opts []ConverseOption) string {
	o := Apply(opts)
	h := sha256.New()
	fmt.Fprintf(h, "model:%s\n", c.inner.Model())
	fmt.Fprintf(h, "system:%s\n", system)
	for _, m := range messages {
		fmt.Fprintf(h, "%s:%s\n", m.Role, m.Content)
	}
	fmt.Fprintf(h, "max_tokens:%d\n", o.MaxTokens)
	fmt.Fprintf(h, "thinking:%d\n", o.ThinkingBudget)
	return fmt.Sprintf("%x", h.Sum(nil))
}

func (c *CachedClient) cachePath(key string) string {
	return filepath.Join(c.dir, key[:2], key+".json")
}

func (c *CachedClient) get(key string) (*cachedEntry, bool) {
	data, err := os.ReadFile(c.cachePath(key))
	if err != nil {
		return nil, false
	}
	var entry cachedEntry
	if err := json.Unmarshal(data, &entry); err != nil {
		return nil, false
	}
	return &entry, true
}

func (c *CachedClient) put(key string, entry *cachedEntry) {
	path := c.cachePath(key)
	c.mu.Lock()
	defer c.mu.Unlock()
	os.MkdirAll(filepath.Dir(path), 0755)
	data, err := json.Marshal(entry)
	if err != nil {
		return
	}
	os.WriteFile(path, data, 0644)
}

// ConverseMessages checks the cache before calling the inner client.
func (c *CachedClient) ConverseMessages(ctx context.Context, system string, messages []Message, opts ...ConverseOption) (*Response, error) {
	key := c.cacheKey(system, messages, opts)

	if entry, ok := c.get(key); ok {
		resp := &Response{
			Text:  entry.Text,
			Usage: NewUsage(entry.InputTokens, entry.OutputTokens, 0), // zero cost on cache hit
		}
		if entry.Truncated {
			return resp, &TruncatedError{OutputTokens: entry.OutputTokens}
		}
		if entry.Error != "" {
			return resp, fmt.Errorf("%s", entry.Error)
		}
		return resp, nil
	}

	resp, err := c.inner.ConverseMessages(ctx, system, messages, opts...)

	// Cache the result (including errors and truncations)
	entry := &cachedEntry{}
	if resp != nil {
		entry.Text = resp.Text
		entry.InputTokens = resp.Usage.InputTokens
		entry.OutputTokens = resp.Usage.OutputTokens
		entry.Cost = resp.Usage.Cost()
	}
	if err != nil {
		if IsTruncated(err) {
			entry.Truncated = true
		} else {
			entry.Error = err.Error()
		}
	}
	c.put(key, entry)

	return resp, err
}

// ConverseMessagesStream bypasses the cache — streaming calls are for
// interactive compose output that changes every run.
func (c *CachedClient) ConverseMessagesStream(ctx context.Context, system string, messages []Message, fn StreamFunc, opts ...ConverseOption) (*Response, error) {
	return c.inner.ConverseMessagesStream(ctx, system, messages, fn, opts...)
}

// Model returns the inner client's model identifier.
func (c *CachedClient) Model() string {
	return c.inner.Model()
}
