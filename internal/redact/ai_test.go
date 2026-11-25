package redact

import (
	"fmt"
	"strings"
	"testing"
)

func newTestEngine(t *testing.T, p Policy) *Engine {
	t.Helper()
	e, err := NewEngine(p)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

// Common AI-provider credential shapes. Every value is synthetic and uses a
// documented prefix/length, so the table fails when a provider format stops
// being detected or a new rule starts stealing the match.
func TestCommonAIAPIKeyFormats(t *testing.T) {
	openAIAdmin := fakeToken("sk-admin-", 48, 2)
	openAILegacy := "sk-EanD1OEkeGb8WP88Jk9MT3Bl" + "bkFJRFjmzE8XAAl7o8IzENyX"
	openAISvc := fakeToken("sk-svcacct-", 48, 4)
	anthropicAPI := fakeToken("sk-ant-api03-", 90, 5)
	anthropicAdmin := fakeToken("sk-ant-admin01-", 90, 6)
	gemini := fakeToken("AIza", 35, 7)
	groq := fakeToken("gsk_", 52, 8)
	deepseek := "sk-duu8wkernulhwb7g" + "0ncfo1otofxycuyg"
	cohere := fakeToken("", 40, 10)
	cerebras := fakeToken("csk-", 40, 11)
	cursor := fakeToken("crsr_", 20, 12)
	huggingface := fakeToken("hf_", 34, 13)
	perplexity := fakeToken("pplx-", 48, 15)
	fireworks := fakeToken("fw_", 40, 17)
	firepass := fakeToken("fpk_", 40, 18)
	tavily := fakeToken("tvly-", 40, 19)
	replicate := fakeToken("r8_", 37, 20)
	nvidia := fakeToken("nvapi-", 64, 21)
	jina := fakeToken("jina_", 64, 22)
	age := fakeUpper("AGE-SECRET-KEY-1", 58, 23)
	gocspx := fakeToken("GOCSPX-", 28, 24)
	databricks := fakeHex("dapi", 32, 25)
	mistral := fakeToken("", 32, 26)
	together := fakeHex("", 64, 27)
	azure := fakeHex("", 32, 28)
	awsSecret := fakeBase64("", 40, 29)
	awsAccess := "AKIAIOSFODNN" + "7EXAMPLQ"
	bedrock := "ABSKQmVkcm9ja0FQSUtleS" + "1JsxxGJaQ4E7VYU861k0Q2JFWurEFcHTB3pWIQsxSkp17IPEFuZJ3LArOxD1u7fB08el5jzT3r5+5VZtUxmlfUSk2VaOi1tSZz3ARZsPFTH+BW"
	privateKeyBody := fakeBase64("", 40, 32)

	tests := []struct {
		name string
		text string
		key  string
		rule string
	}{
		{
			name: "openai project",
			text: "OPENAI_API_KEY=" + openAIProject,
			key:  openAIProject,
			rule: "openai-project-key",
		},
		{
			name: "openai admin",
			text: "OPENAI_API_KEY=" + openAIAdmin,
			key:  openAIAdmin,
			rule: "openai-project-key",
		},
		{
			name: "openai project bare",
			text: openAIProject,
			key:  openAIProject,
			rule: "openai-project-key",
		},
		{
			name: "openai legacy",
			text: "OPENAI_API_KEY=" + openAILegacy,
			key:  openAILegacy,
			rule: "openai-legacy-key",
		},
		{
			name: "openai service account",
			text: "OPENAI_API_KEY=" + openAISvc,
			key:  openAISvc,
			rule: "openai-project-key",
		},
		{
			name: "anthropic api",
			text: "ANTHROPIC_API_KEY=" + anthropicAPI,
			key:  anthropicAPI,
			rule: "anthropic-api-key",
		},
		{
			name: "anthropic admin",
			text: "ANTHROPIC_API_KEY=" + anthropicAdmin,
			key:  anthropicAdmin,
			rule: "anthropic-api-key",
		},
		{
			name: "google gemini",
			text: "GOOGLE_API_KEY=" + gemini,
			key:  gemini,
			rule: "gcp-api-key",
		},
		{
			name: "groq",
			text: "GROQ_API_KEY=" + groq,
			key:  groq,
			rule: "groq-api-key",
		},
		{
			name: "deepseek",
			text: "DEEPSEEK_API_KEY=" + deepseek,
			key:  deepseek,
			rule: "deepseek-api-key",
		},
		{
			name: "cohere",
			text: "CO_API_KEY=" + cohere,
			key:  cohere,
			rule: "cohere-api-key",
		},
		{
			name: "cerebras",
			text: "CEREBRAS_API_KEY=" + cerebras,
			key:  cerebras,
			rule: "cerebras-api-key",
		},
		{
			name: "cursor",
			text: "CURSOR_API_KEY=" + cursor,
			key:  cursor,
			rule: "cursor-api-key",
		},
		{
			name: "hugging face",
			text: "HUGGINGFACE_TOKEN=" + huggingface,
			key:  huggingface,
			rule: "huggingface-token",
		},
		{
			name: "xai bare",
			text: xai,
			key:  xai,
			rule: "xai-api-key",
		},
		{
			name: "perplexity bare",
			text: perplexity,
			key:  perplexity,
			rule: "perplexity-api-key",
		},
		{
			name: "openrouter bare",
			text: openrouter,
			key:  openrouter,
			rule: "openrouter-api-key",
		},
		{
			name: "fireworks bare",
			text: fireworks,
			key:  fireworks,
			rule: "fireworks-api-key",
		},
		{
			name: "fireworks firepass",
			text: firepass,
			key:  firepass,
			rule: "fireworks-firepass-key",
		},
		{
			name: "tavily bare",
			text: tavily,
			key:  tavily,
			rule: "tavily-api-key",
		},
		{
			name: "replicate bare",
			text: replicate,
			key:  replicate,
			rule: "replicate-api-token",
		},
		{
			name: "nvidia bare",
			text: nvidia,
			key:  nvidia,
			rule: "nvidia-api-key",
		},
		{
			name: "jina bare",
			text: jina,
			key:  jina,
			rule: "jina-api-key",
		},
		{
			name: "age secret key",
			text: age,
			key:  age,
			rule: "age-secret-key",
		},
		{
			name: "google oauth client secret",
			text: gocspx,
			key:  gocspx,
			rule: "google-oauth-client-secret",
		},
		{
			name: "databricks bare",
			text: databricks,
			key:  databricks,
			rule: "databricks-api-token",
		},
		{
			name: "mistral contextual",
			text: "MISTRAL_API_KEY=" + mistral,
			key:  mistral,
			rule: "mistral-api-key",
		},
		{
			name: "together contextual",
			text: `TOGETHER_API_KEY="` + together + `"`,
			key:  together,
			rule: "together-api-key",
		},
		{
			name: "azure openai contextual",
			text: "AZURE_OPENAI_API_KEY=" + azure,
			key:  mistral,
			rule: "azure-openai-api-key",
		},
		{
			name: "aws secret access key",
			text: "aws_secret_access_key = '" + awsSecret + "'",
			key:  awsSecret,
			rule: "aws-secret-access-key",
		},
		{
			name: "aws access key",
			text: "AWS_ACCESS_KEY_ID=" + awsAccess,
			key:  awsAccess,
			rule: "aws-access-key-id",
		},
		{
			name: "bedrock api key",
			text: "AWS_BEARER_TOKEN_BEDROCK=" + bedrock,
			key:  bedrock,
			rule: "aws-bedrock-long-lived-api-key",
		},
		{
			name: "private key",
			text: "-----BEGIN OPENSSH PRIVATE KEY-----\n" + privateKeyBody + "\n-----END OPENSSH PRIVATE KEY-----",
			key:  privateKeyBody,
			rule: "pem-private-key",
		},
	}

	engine := newTestEngine(t, DefaultPolicy())
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result, err := engine.Redact(tc.text)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(result.Text, tc.key) {
				t.Fatalf("secret survived redaction: %q", result.Text)
			}
			want := placeholder(tc.rule)
			if !strings.Contains(result.Text, want) {
				t.Fatalf("redacted text %q missing %s", result.Text, want)
			}
			if result.Counts[tc.rule] == 0 {
				t.Fatalf("rule %s not counted: %+v", tc.rule, result.Counts)
			}
			if got := fmt.Sprintf("%+v", result); strings.Contains(got, tc.key) {
				t.Fatalf("result metadata contains the secret: %s", got)
			}
		})
	}
}

func TestRedactionDoesNotSwallowSurroundingText(t *testing.T) {
	engine := newTestEngine(t, DefaultPolicy())
	key := openAIProject
	text := "before `" + key + "` after."
	result, err := engine.Redact(text)
	if err != nil {
		t.Fatal(err)
	}
	want := "before `[REDACTED:OPENAI_PROJECT_KEY]` after."
	if result.Text != want {
		t.Fatalf("redacted = %q, want %q", result.Text, want)
	}
}

func TestFalsePositiveShapesRemainUnchanged(t *testing.T) {
	engine := newTestEngine(t, DefaultPolicy())
	for _, text := range []string{
		"sk-short",
		"sk-proj-too-short",
		"xai-short",
		"pplx-short",
		"fw_short",
		"r8_short",
		"nvapi-short",
		"jina_short",
		"AGE-SECRET-KEY-1short",
		"GOCSPX-short",
		"dapi1234",
		"sk-or-v1-" + fakeHex("", 32, 99),
		"prefix x" + xai,
		"MISTRAL_API_KEY=your_api_key_here",
		"AZURE_OPENAI_API_KEY=${AZURE_OPENAI_API_KEY}",
		"TOGETHER_API_KEY=changeme",
		"PASSWORD=changeme",
	} {
		result, err := engine.Redact(text)
		if err != nil {
			t.Fatalf("%q: %v", text, err)
		}
		if len(result.Findings) != 0 || result.Text != text {
			t.Errorf("%q changed: %q %+v", text, result.Text, result.Findings)
		}
	}
}

func TestOpenRouterIsNotPartiallyRedactedAsOpenAI(t *testing.T) {
	engine := newTestEngine(t, DefaultPolicy())
	key := openrouter
	result, err := engine.Redact(key)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Findings) != 1 || result.Findings[0].RuleID != "openrouter-api-key" {
		t.Fatalf("findings = %+v", result.Findings)
	}
	if result.Text != "[REDACTED:OPENROUTER_API_KEY]" {
		t.Fatalf("redacted = %q", result.Text)
	}
}

func TestMoreSpecificSupplementWinsOverGenericAssignment(t *testing.T) {
	engine := newTestEngine(t, DefaultPolicy())
	result, err := engine.Redact("FIREWORKS_API_KEY=" + fakeToken("fw_", 40, 17))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Findings) != 1 || result.Findings[0].RuleID != "fireworks-api-key" {
		t.Fatalf("findings = %+v", result.Findings)
	}
	if result.Text != "FIREWORKS_API_KEY=[REDACTED:FIREWORKS_API_KEY]" {
		t.Fatalf("redacted = %q", result.Text)
	}
}

func TestVersionIsStableAndProfileSensitive(t *testing.T) {
	balanced := newTestEngine(t, DefaultPolicy())
	deep := newTestEngine(t, Policy{Profile: ProfileDeep, MaxInputBytes: DefaultMaxInputBytes})
	key := xai
	a, err := balanced.Redact(key)
	if err != nil {
		t.Fatal(err)
	}
	b, err := balanced.Redact(key)
	if err != nil {
		t.Fatal(err)
	}
	if a.Text != b.Text || a.Version != b.Version || a.Version != balanced.Version() {
		t.Fatalf("unstable result: %+v / %+v", a, b)
	}
	if balanced.Version() == deep.Version() {
		t.Fatal("profile is not part of the redaction version")
	}
	if !strings.HasPrefix(a.Version, "rd2-") {
		t.Fatalf("version = %q", a.Version)
	}
}

func TestInputTooLargeFailsClosed(t *testing.T) {
	engine := newTestEngine(t, Policy{Profile: ProfileBalanced, MaxInputBytes: 16})
	key := xai
	_, err := engine.Redact(key)
	if err == nil {
		t.Fatal("oversized input accepted")
	}
	if !strings.Contains(err.Error(), "exceeds limit") {
		t.Fatalf("error = %v", err)
	}
	if strings.Contains(err.Error(), key) {
		t.Fatal("error contains the input")
	}
}
