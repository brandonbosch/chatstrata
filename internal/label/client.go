package label

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	// DefaultModel pins an explicit Jev release so calibrated probabilities do
	// not drift silently across jev-latest updates (ADR 0007).
	DefaultModel = "jev-1.14.0"

	// DefaultBaseURL is TypeSafe's System One API base URL.
	DefaultBaseURL = "https://api.typesafe.ai"

	// DefaultPricePerMTok is the reference input-token list price (USD per
	// million input tokens; output tokens are free) used for pre-run estimates.
	DefaultPricePerMTok = 0.042
)

// FatalError marks a backend failure that must abort the entire run (such as
// invalid authentication or missing API key) rather than skipping one target.
type FatalError struct {
	Err error
}

func (e *FatalError) Error() string { return e.Err.Error() }
func (e *FatalError) Unwrap() error { return e.Err }

// AskResult is the structured answer returned for one target.
type AskResult struct {
	Model       string
	Answers     map[string]Answer
	InputTokens int64
}

// Backend evaluates one redacted target state against a pack's questions.
type Backend interface {
	Name() string
	Ask(ctx context.Context, state map[string]any, questions map[string]QuestionSpec) (AskResult, error)
}

// TypeSafeClient calls TypeSafe's POST /v1/systemone endpoint using net/http.
type TypeSafeClient struct {
	apiKey     string
	baseURL    string
	model      string
	httpClient *http.Client
	maxRetries int
	baseDelay  time.Duration
}

// ClientOptions configures TypeSafeClient.
type ClientOptions struct {
	APIKey     string
	BaseURL    string
	Model      string
	HTTPClient *http.Client
	MaxRetries int
	BaseDelay  time.Duration
}

// NewTypeSafeClient builds a TypeSafe System One client. When APIKey is empty
// it reads TYPESAFE_API_KEY from the environment.
func NewTypeSafeClient(opts ClientOptions) (*TypeSafeClient, error) {
	key := strings.TrimSpace(opts.APIKey)
	if key == "" {
		key = strings.TrimSpace(os.Getenv("TYPESAFE_API_KEY"))
	}
	if key == "" {
		return nil, &FatalError{Err: errors.New("TYPESAFE_API_KEY is not set; create a key at https://console.typesafe.ai/keys")}
	}
	baseURL := strings.TrimSpace(opts.BaseURL)
	if baseURL == "" {
		baseURL = strings.TrimSpace(os.Getenv("TYPESAFE_BASE_URL"))
	}
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	baseURL = strings.TrimRight(baseURL, "/")

	model := strings.TrimSpace(opts.Model)
	if model == "" {
		model = DefaultModel
	}
	hc := opts.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: 60 * time.Second}
	}
	retries := opts.MaxRetries
	if retries <= 0 {
		retries = 3
	}
	delay := opts.BaseDelay
	if delay <= 0 {
		delay = 500 * time.Millisecond
	}
	return &TypeSafeClient{
		apiKey:     key,
		baseURL:    baseURL,
		model:      model,
		httpClient: hc,
		maxRetries: retries,
		baseDelay:  delay,
	}, nil
}

// Name returns the backend identifier recorded in label_runs.
func (c *TypeSafeClient) Name() string { return "typesafe" }

type systemOneRequest struct {
	Model     string                  `json:"model"`
	State     map[string]any          `json:"state"`
	Questions map[string]QuestionSpec `json:"questions"`
}

type systemOneResponse struct {
	Model   string            `json:"model"`
	Answers map[string]Answer `json:"answers"`
	Usage   struct {
		InputTokens int64 `json:"input_tokens"`
	} `json:"usage"`
}

type apiErrorBody struct {
	Error any    `json:"error"`
	Msg   string `json:"message"`
}

// Ask sends one redacted state and question map to POST /v1/systemone.
func (c *TypeSafeClient) Ask(ctx context.Context, state map[string]any, questions map[string]QuestionSpec) (AskResult, error) {
	body, err := json.Marshal(systemOneRequest{
		Model:     c.model,
		State:     state,
		Questions: questions,
	})
	if err != nil {
		return AskResult{}, fmt.Errorf("marshal systemone request: %w", err)
	}

	endpoint := c.baseURL + "/v1/systemone"
	var lastErr error
	for attempt := 0; attempt <= c.maxRetries; attempt++ {
		if err := ctx.Err(); err != nil {
			return AskResult{}, err
		}
		res, retryAfter, retryable, err := c.doOnce(ctx, endpoint, body)
		if err == nil {
			return res, nil
		}
		lastErr = err
		var fatal *FatalError
		if errors.As(err, &fatal) || !retryable || attempt == c.maxRetries {
			return AskResult{}, err
		}
		wait := retryAfter
		if wait <= 0 {
			wait = c.baseDelay << attempt
		}
		if wait > 15*time.Second {
			wait = 15 * time.Second
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return AskResult{}, ctx.Err()
		case <-timer.C:
		}
	}
	return AskResult{}, lastErr
}

func (c *TypeSafeClient) doOnce(ctx context.Context, endpoint string, body []byte) (AskResult, time.Duration, bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return AskResult{}, 0, false, fmt.Errorf("build systemone request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "chatstrata")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return AskResult{}, 0, true, fmt.Errorf("call TypeSafe API: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 4*1024*1024))
	if err != nil {
		return AskResult{}, 0, true, fmt.Errorf("read TypeSafe response: %w", err)
	}

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return AskResult{}, 0, false, &FatalError{
			Err: fmt.Errorf("TypeSafe rejected the API key (HTTP %d): %s", resp.StatusCode, summarizeAPIError(respBody)),
		}
	}
	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
		return AskResult{}, parseRetryAfter(resp.Header.Get("Retry-After")), true,
			fmt.Errorf("TypeSafe API HTTP %d: %s", resp.StatusCode, summarizeAPIError(respBody))
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return AskResult{}, 0, false,
			fmt.Errorf("TypeSafe API HTTP %d: %s", resp.StatusCode, summarizeAPIError(respBody))
	}

	var parsed systemOneResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return AskResult{}, 0, false, fmt.Errorf("decode TypeSafe response: %w", err)
	}
	if len(parsed.Answers) == 0 {
		return AskResult{}, 0, false, errors.New("TypeSafe response contained no answers")
	}
	model := parsed.Model
	if model == "" {
		model = c.model
	}
	return AskResult{
		Model:       model,
		Answers:     parsed.Answers,
		InputTokens: parsed.Usage.InputTokens,
	}, 0, false, nil
}

func parseRetryAfter(h string) time.Duration {
	h = strings.TrimSpace(h)
	if h == "" {
		return 0
	}
	if secs, err := strconv.Atoi(h); err == nil && secs > 0 {
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(h); err == nil {
		if d := time.Until(t); d > 0 {
			return d
		}
	}
	return 0
}

func summarizeAPIError(body []byte) string {
	var parsed apiErrorBody
	if json.Unmarshal(body, &parsed) == nil {
		if parsed.Msg != "" {
			return clipText(parsed.Msg, 200)
		}
		if parsed.Error != nil {
			switch v := parsed.Error.(type) {
			case string:
				if v != "" {
					return clipText(v, 200)
				}
			case map[string]any:
				if msg, ok := v["message"].(string); ok && msg != "" {
					return clipText(msg, 200)
				}
			}
		}
	}
	s := strings.TrimSpace(string(body))
	if s == "" {
		return "empty response"
	}
	return clipText(s, 200)
}
