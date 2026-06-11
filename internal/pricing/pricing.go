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

// ModelInfo is a catalog entry exposed for what-if comparisons.
type ModelInfo struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Price    Price  `json:"price"`
}

// catalogProviders are the canonical (non-router) providers listed for
// what-if model comparisons.
var catalogProviders = map[string]bool{
	"anthropic": true, "openai": true, "google": true, "google-vertex": true,
	"mistral": true, "deepseek": true, "groq": true, "cohere": true,
	"xai": true, "amazon-bedrock": true, "azure": true, "meta": true,
}

// ListModels returns priced models from canonical providers, for use as
// what-if targets.
func ListModels() []ModelInfo {
	once.Do(load)
	var result []ModelInfo
	var catalog map[string]catalogProvider
	if err := json.Unmarshal(modelsJSON, &catalog); err != nil {
		return result
	}
	for provider, p := range catalog {
		if !catalogProviders[provider] {
			continue
		}
		for model, m := range p.Models {
			if m.Cost == nil {
				continue
			}
			result = append(result, ModelInfo{Provider: provider, Model: model, Price: *m.Cost})
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Provider != result[j].Provider {
			return result[i].Provider < result[j].Provider
		}
		return result[i].Model < result[j].Model
	})
	return result
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
