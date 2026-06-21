// Package pricing resolves model token prices from the models.dev catalog.
// The catalog (models.json) is a snapshot of https://models.dev/api.json,
// embedded at build time; refresh it with `task pricing-update`.
package pricing

import (
	_ "embed"
	"encoding/json"
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
	once     sync.Once
	byKey    map[string]Price // "provider/model"
	byModel  map[string]Price // model id only, first canonical provider wins
)

// providerAliases maps gen_ai.system / gen_ai.provider.name values to
// models.dev provider keys where they differ.
var providerAliases = map[string]string{
	"gcp.gemini":      "google",
	"gcp.vertex_ai":   "google-vertex",
	"aws.bedrock":     "amazon-bedrock",
	"azure.ai.openai": "azure",
}

func load() {
	var catalog map[string]catalogProvider
	if err := json.Unmarshal(modelsJSON, &catalog); err != nil {
		byKey, byModel = map[string]Price{}, map[string]Price{}
		return
	}
	byKey = make(map[string]Price)
	byModel = make(map[string]Price)
	for provider, p := range catalog {
		for model, m := range p.Models {
			if m.Cost == nil {
				continue
			}
			byKey[strings.ToLower(provider+"/"+model)] = *m.Cost
			// For the provider-less index, prefer the entry from the model's
			// own provider; routers (requesty, openrouter, ...) namespace ids
			// with "provider/" so plain ids mostly come from canonical sources.
			key := strings.ToLower(model)
			if _, exists := byModel[key]; !exists || !strings.Contains(model, "/") {
				byModel[key] = *m.Cost
			}
		}
	}
}

// Lookup resolves a price for a (provider, model) pair as reported via OTel
// gen_ai attributes. Falls back to a provider-agnostic model match.
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
	return Price{}, false
}
