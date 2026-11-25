package redact

import (
	"strings"
	"sync"
	"testing"
)

func TestAgeSecretKeyVariants(t *testing.T) {
	engine := newTestEngine(t, DefaultPolicy())
	for _, key := range []string{
		fakeUpper("AGE-SECRET-KEY-1", 58, 33),
		fakeUpper("AGE-SECRET-KEY-PQ-1", 58, 34),
	} {
		result, err := engine.Redact(key)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(result.Text, key) || result.Text != "[REDACTED:AGE_SECRET_KEY]" {
			t.Errorf("%q -> %q", key, result.Text)
		}
	}
}

func TestEngineIsSafeForConcurrentUse(t *testing.T) {
	engine := newTestEngine(t, DefaultPolicy())
	texts := []string{
		"key " + openAIProject,
		"password = \"hunter2\"",
		"contact jordan@example.com",
		"nothing to redact here",
	}
	var wg sync.WaitGroup
	for range 32 {
		for _, text := range texts {
			wg.Add(1)
			go func(text string) {
				defer wg.Done()
				result, err := engine.Redact(text)
				if err != nil {
					t.Errorf("%q: %v", text, err)
					return
				}
				if strings.Contains(result.Text, "sk-proj-") || strings.Contains(result.Text, "hunter2") {
					t.Errorf("secret survived: %q", result.Text)
				}
			}(text)
		}
	}
	wg.Wait()
}

func TestRuleIDsIncludeSupplementalRules(t *testing.T) {
	engine := newTestEngine(t, DefaultPolicy())
	ids := strings.Join(engine.RuleIDs(), "\n")
	for _, want := range []string{
		"openai-project-key", "anthropic-api-key", "xai-api-key",
		"openrouter-api-key", "password-assignment", "age-secret-key",
	} {
		if !strings.Contains(ids, want) {
			t.Errorf("rule list missing %s", want)
		}
	}
}
