package pricing

import (
	"reflect"
	"testing"
)

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
			if priced && !reflect.DeepEqual(got, tt.want) {
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
		if !ok || !reflect.DeepEqual(got, want) {
			t.Fatalf("provider-less gpt-5.1 = %+v (ok=%v), want canonical %+v", got, ok, want)
		}
	}
}

// TestLongContextTiers exercises the catalog's cost.tiers parsing and tier
// selection: gpt-5.5 carries a context tier at 272k input tokens, gpt-5.1 is
// flat. As in TestLookup, expectations resolve from the catalog itself rather
// than hard-coding rates.
func TestLongContextTiers(t *testing.T) {
	flat := mustLookup(t, "openai", "gpt-5.1")
	if len(flat.Tiers) != 0 {
		t.Fatalf("gpt-5.1 should be flat-priced, got tiers %+v", flat.Tiers)
	}
	if got := flat.ForInput(1_000_000); !reflect.DeepEqual(got, flat) {
		t.Fatalf("flat model changed rates for huge input: %+v", got)
	}

	tiered := mustLookup(t, "openai", "gpt-5.5")
	if len(tiered.Tiers) == 0 {
		t.Fatal("gpt-5.5 should carry a long-context tier")
	}
	top := tiered.Tiers[len(tiered.Tiers)-1]
	if top.Threshold != 272_000 {
		t.Fatalf("gpt-5.5 tier threshold = %v, want 272000", top.Threshold)
	}
	if top.Input <= tiered.Input {
		t.Fatalf("tier input rate %v not above base %v", top.Input, tiered.Input)
	}

	// At the threshold: base rates. One token above: the tier card.
	at := tiered.ForInput(top.Threshold)
	if at.Input != tiered.Input || at.Output != tiered.Output {
		t.Fatalf("at threshold = %+v, want base rates %+v", at, tiered)
	}
	over := tiered.ForInput(top.Threshold + 1)
	if over.Input != top.Input || over.Output != top.Output || over.CacheRead != top.CacheRead {
		t.Fatalf("over threshold = %+v, want tier rates %+v", over, top)
	}

	// A rate the catalog omits on the tier entry inherits the base rate
	// (requesty's gemini-2.5-pro tier has no cache_write; the base does).
	gem := mustLookup(t, "requesty", "google/gemini-2.5-pro")
	if len(gem.Tiers) == 0 {
		t.Fatal("requesty google/gemini-2.5-pro should carry a context tier")
	}
	if gem.CacheWrite == 0 {
		t.Fatal("test premise broken: base cache_write is zero")
	}
	if got := gem.Tiers[0].CacheWrite; got != gem.CacheWrite {
		t.Fatalf("omitted tier cache_write = %v, want base %v", got, gem.CacheWrite)
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
