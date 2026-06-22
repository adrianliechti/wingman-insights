// Package pricing resolves model token prices from the models.dev catalog.
// The catalog (models.json) is a snapshot of https://models.dev/api.json,
// embedded at build time; refresh it with `task pricing-update`.
package pricing

import (
	_ "embed"
	"encoding/json"
	"sort"
	"strings"
	"sync"
)

//go:embed models.json
var modelsJSON []byte

// Price holds USD cost per one million tokens.
type Price struct {
	Input      float64 `json:"input"`
	Output     float64 `json:"output"`
	CacheRead  float64 `json:"cache_read"`
	CacheWrite float64 `json:"cache_write"`
}

// TokenCost returns the USD cost for n tokens of the given OTel gen_ai.token.type.
func (p Price) TokenCost(tokenType string, n float64) float64 {
	perMillion := map[string]float64{
		"input":          p.Input,
		"output":         p.Output,
		"cache_read":     p.CacheRead,
		"cache_creation": p.CacheWrite,
	}[tokenType]
	return n / 1_000_000 * perMillion
}

// Cost prices a full token breakdown for one request (or an aggregate),
// accounting for provider-managed cache. Per the OTel GenAI semconv,
// gen_ai.usage.input_tokens is the inclusive prompt total — it already contains
// cacheRead + cacheCreation — so only the non-cached remainder is billed at the
// input rate, while cached tokens bill at their own (cheaper) rates. Reasoning
// tokens are billed within output and are not added separately.
func (p Price) Cost(input, output, cacheRead, cacheCreation float64) float64 {
	regular := input - cacheRead - cacheCreation
	if regular < 0 {
		regular = 0
	}
	return p.TokenCost("input", regular) +
		p.TokenCost("cache_read", cacheRead) +
		p.TokenCost("cache_creation", cacheCreation) +
		p.TokenCost("output", output)
}

type catalogModel struct {
	Cost *Price `json:"cost"`
}

type catalogProvider struct {
	Models map[string]catalogModel `json:"models"`
}

var (
	once    sync.Once
	byKey   map[string]Price    // "provider/model" exact
	byModel map[string]Price    // model id only; canonical provider wins (deterministic)
	provIDs map[string][]string // provider -> its model ids, longest first (best-effort match)
)

// providerPriority orders the providers consulted for provider-less and
// best-effort matches. First-party houses come first so an ambiguous or
// user-aliased model prices at the authoritative rate, not a random reseller's.
// A model offered only by an unlisted provider is still matched exactly via
// byKey (provider+model); it is just never guessed into.
var providerPriority = []string{
	"openai", "anthropic", "google", "google-vertex", "google-vertex-anthropic",
	"xai", "mistral", "llama", "deepseek", "cohere", "perplexity",
	"amazon-bedrock", "azure", "groq", "nvidia", "openrouter",
}

// providerAliases maps gen_ai.provider.name values to models.dev provider keys
// where they differ. The primary source is wingman, which reports its provider
// config Type (config/config_completer.go) — not OTel semconv names — so those
// are mapped first. The semconv well-known values are kept as a defensive
// fallback for any other OTLP source. Wingman types that already equal a
// models.dev key (anthropic, google, openai, xai, mistral, llama, nvidia,
// openrouter) need no entry; ollama / openai-compatible / custom have no
// catalog and stay unpriced.
var providerAliases = map[string]string{
	// wingman provider config types
	"bedrock": "amazon-bedrock",
	"gemini":  "google",
	"nim":     "nvidia",
	// OTel semconv well-known gen_ai.provider.name values
	"gcp.gemini":         "google",
	"gcp.gen_ai":         "google",
	"gcp.vertex_ai":      "google-vertex",
	"aws.bedrock":        "amazon-bedrock",
	"azure.ai.openai":    "azure",
	"azure.ai.inference": "azure",
	"x_ai":               "xai",
	"mistral_ai":         "mistral",
}

func load() {
	var catalog map[string]catalogProvider
	if err := json.Unmarshal(modelsJSON, &catalog); err != nil {
		byKey, byModel, provIDs = map[string]Price{}, map[string]Price{}, map[string][]string{}
		return
	}
	byKey = make(map[string]Price)
	byModel = make(map[string]Price)
	provIDs = make(map[string][]string)
	for provider, p := range catalog {
		lp := strings.ToLower(provider)
		for model, m := range p.Models {
			if m.Cost == nil {
				continue
			}
			lm := strings.ToLower(model)
			byKey[lp+"/"+lm] = *m.Cost
			provIDs[lp] = append(provIDs[lp], lm)
		}
	}
	// Longest id first so a bounded-substring scan returns the most specific
	// match (gpt-5.5 before gpt-5) on the first hit.
	for _, ids := range provIDs {
		sort.Slice(ids, func(i, j int) bool {
			if len(ids[i]) != len(ids[j]) {
				return len(ids[i]) > len(ids[j])
			}
			return ids[i] < ids[j]
		})
	}
	// byModel is the provider-less exact index, resolved deterministically:
	// priority houses first, then the rest alphabetically, first writer wins.
	order := append([]string{}, providerPriority...)
	order = append(order, remainingProviders()...)
	for _, prov := range order {
		for _, id := range provIDs[prov] {
			if _, ok := byModel[id]; !ok {
				byModel[id] = byKey[prov+"/"+id]
			}
		}
	}
}

// remainingProviders returns the catalog providers not in providerPriority,
// sorted, so byModel construction is fully deterministic.
func remainingProviders() []string {
	inPriority := make(map[string]bool, len(providerPriority))
	for _, p := range providerPriority {
		inPriority[p] = true
	}
	var rest []string
	for p := range provIDs {
		if !inPriority[p] {
			rest = append(rest, p)
		}
	}
	sort.Strings(rest)
	return rest
}

// Lookup resolves a price for a (provider, model) pair as reported via OTel
// gen_ai attributes. It tries, in order: an exact provider+model match, an
// exact provider-less model match, then a best-effort match that recovers a
// user-configured alias by finding the catalog model id embedded in it. The
// alias may decorate the base id at either end — "gpt-5.5-se" (a regional
// deployment suffix) and "se.gpt-5.5" both resolve to "gpt-5.5".
func Lookup(provider, model string) (Price, bool) {
	once.Do(load)
	if model == "" {
		return Price{}, false
	}
	provider = strings.ToLower(provider)
	if alias, ok := providerAliases[provider]; ok {
		provider = alias
	}
	model = strings.ToLower(model)

	if p, ok := byKey[provider+"/"+model]; ok {
		return p, true
	}
	if p, ok := byModel[model]; ok {
		return p, true
	}
	// Best-effort: the named provider is authoritative, so try its catalog
	// first; otherwise take the longest (most specific) match among the
	// priority houses. Re-scanning the named provider in the loop is harmless —
	// it already missed above, so it can't match again.
	if id, ok := longestBounded(provIDs[provider], model); ok {
		return byKey[provider+"/"+id], true
	}
	var best Price
	bestLen := 0
	for _, prov := range providerPriority {
		if id, ok := longestBounded(provIDs[prov], model); ok && len(id) > bestLen {
			best, bestLen = byKey[prov+"/"+id], len(id)
		}
	}
	return best, bestLen > 0
}

// longestBounded returns the longest id in ids (already sorted longest-first)
// that occurs in model as a separator-anchored token — so "gpt-5.5" matches
// "gpt-5.5-se" and "se.gpt-5.5" but "gpt-5" never matches "gpt-50". Ids shorter
// than three chars are skipped to avoid spurious substring hits.
func longestBounded(ids []string, model string) (string, bool) {
	for _, id := range ids {
		if len(id) >= 3 && boundedContains(model, id) {
			return id, true
		}
	}
	return "", false
}

// boundedContains reports whether id appears in s delimited on both sides by a
// separator or a string edge.
func boundedContains(s, id string) bool {
	for from := 0; ; {
		i := strings.Index(s[from:], id)
		if i < 0 {
			return false
		}
		start := from + i
		end := start + len(id)
		leftOK := start == 0 || isSep(s[start-1])
		rightOK := end == len(s) || isSep(s[end])
		if leftOK && rightOK {
			return true
		}
		from = start + 1
	}
}

func isSep(b byte) bool {
	switch b {
	case '-', '.', ':', '/', '@', '_', ' ':
		return true
	}
	return false
}
