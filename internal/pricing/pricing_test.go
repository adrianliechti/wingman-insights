package pricing

import "testing"

func TestLookup(t *testing.T) {
	// Resolve the canonical catalog prices we assert against, so the test
	// tracks catalog refreshes instead of hard-coding numbers.
	gpt51, ok := Lookup("openai", "gpt-5.1")
	if !ok {
		t.Fatal("openai/gpt-5.1 not in catalog")
	}
	gpt55, ok := Lookup("openai", "gpt-5.5")
	if !ok {
		t.Fatal("openai/gpt-5.5 not in catalog")
	}
	gemini, ok := Lookup("google", "gemini-2.5-pro")
	if !ok {
		t.Fatal("google/gemini-2.5-pro not in catalog")
	}

	tests := []struct {
		name       string
		provider   string
		model      string
		wantPriced bool
		want       Price
	}{
		{"exact", "openai", "gpt-5.1", true, gpt51},
		{"case-insensitive", "OpenAI", "GPT-5.1", true, gpt51},

		// wingman provider Type -> models.dev key aliases.
		{"alias gemini->google", "gemini", "gemini-2.5-pro", true, gemini},
		// A bedrock-only id (region/version decorated) resolves only because the
		// alias maps bedrock -> amazon-bedrock before the exact lookup.
		{"alias bedrock->amazon-bedrock", "bedrock", "us.meta.llama4-scout-17b-instruct-v1:0", true,
			mustLookup(t, "amazon-bedrock", "us.meta.llama4-scout-17b-instruct-v1:0")},
		{"semconv x_ai->xai", "x_ai", "grok-4", true, mustLookup(t, "xai", "grok-4")},

		// Best-effort: user-configured alias decorated at either end.
		{"suffix decoration", "openai", "gpt-5.5-se", true, gpt55},
		{"prefix decoration", "openai", "se.gpt-5.5", true, gpt55},
		{"both ends", "openai", "se.gpt-5.5.eu", true, gpt55},
		{"generic provider falls back to priority house", "openai-compatible", "gpt-5.5-se", true, gpt55},

		// Specificity: longer base id wins over its own prefix.
		{"prefers longest base", "openai", "gpt-5.5-mini-xx", true, mustLookup(t, "openai", "gpt-5.5-mini")},

		// Negative cases.
		{"empty model", "openai", "", false, Price{}},
		{"unknown model", "openai", "totally-made-up-9000", false, Price{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, priced := Lookup(tt.provider, tt.model)
			if priced != tt.wantPriced {
				t.Fatalf("priced = %v, want %v (got %+v)", priced, tt.wantPriced, got)
			}
			if priced && got != tt.want {
				t.Fatalf("price = %+v, want %+v", got, tt.want)
			}
		})
	}
}

// TestLookupDeterministic guards the byModel index against the map-iteration
// non-determinism it had before: gpt-5.1 is defined by ~10 providers, and the
// provider-less lookup must always resolve to the canonical openai price.
func TestLookupDeterministic(t *testing.T) {
	want, ok := Lookup("openai", "gpt-5.1")
	if !ok {
		t.Fatal("openai/gpt-5.1 not in catalog")
	}
	for i := 0; i < 50; i++ {
		got, ok := Lookup("", "gpt-5.1")
		if !ok || got != want {
			t.Fatalf("provider-less gpt-5.1 = %+v (ok=%v), want canonical %+v", got, ok, want)
		}
	}
}

func mustLookup(t *testing.T, provider, model string) Price {
	t.Helper()
	p, ok := Lookup(provider, model)
	if !ok {
		t.Fatalf("%s/%s not in catalog", provider, model)
	}
	return p
}
