package channel

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

type openAIRateLimitPair struct {
	requests int64
	tokens   int64
}

var openAIRateLimitTiersByFamily = map[string]map[openAIRateLimitPair]string{
	"gpt-4.1": {
		{requests: 500, tokens: 30_000}:        "T1",
		{requests: 5_000, tokens: 450_000}:     "T2",
		{requests: 5_000, tokens: 800_000}:     "T3",
		{requests: 10_000, tokens: 2_000_000}:  "T4",
		{requests: 10_000, tokens: 30_000_000}: "T5",
	},
	"gpt-4.1-small": {
		{requests: 500, tokens: 200_000}:        "T1",
		{requests: 5_000, tokens: 2_000_000}:    "T2",
		{requests: 5_000, tokens: 4_000_000}:    "T3",
		{requests: 10_000, tokens: 10_000_000}:  "T4",
		{requests: 30_000, tokens: 150_000_000}: "T5",
	},
	"gpt-5": {
		{requests: 500, tokens: 500_000}:       "T1",
		{requests: 5_000, tokens: 1_000_000}:   "T2",
		{requests: 5_000, tokens: 2_000_000}:   "T3",
		{requests: 10_000, tokens: 4_000_000}:  "T4",
		{requests: 15_000, tokens: 40_000_000}: "T5",
	},
	"gpt-5-mini": {
		{requests: 500, tokens: 500_000}:        "T1",
		{requests: 5_000, tokens: 2_000_000}:    "T2",
		{requests: 5_000, tokens: 4_000_000}:    "T3",
		{requests: 10_000, tokens: 10_000_000}:  "T4",
		{requests: 30_000, tokens: 180_000_000}: "T5",
	},
	"gpt-5-nano": {
		{requests: 500, tokens: 200_000}:        "T1",
		{requests: 5_000, tokens: 2_000_000}:    "T2",
		{requests: 5_000, tokens: 4_000_000}:    "T3",
		{requests: 10_000, tokens: 10_000_000}:  "T4",
		{requests: 30_000, tokens: 180_000_000}: "T5",
	},
	"gpt-5.5-pro": {
		{requests: 50, tokens: 50_000}:       "T1",
		{requests: 500, tokens: 200_000}:     "T2",
		{requests: 500, tokens: 500_000}:     "T3",
		{requests: 1_000, tokens: 1_000_000}: "T4",
		{requests: 2_000, tokens: 4_000_000}: "T5",
	},
}

func buildOpenAIValidationResult(endpoint *url.URL, model string, headers http.Header) KeyValidationResult {
	result := KeyValidationResult{
		IsValid:             true,
		OpenAIModel:         model,
		OpenAIRequestsLimit: headers.Get("x-ratelimit-limit-requests"),
		OpenAITokensLimit:   headers.Get("x-ratelimit-limit-tokens"),
	}
	if endpoint != nil {
		result.OpenAIHost = endpoint.Hostname()
	}

	if !isOfficialOpenAIAPI(endpoint) {
		result.OpenAITierReason = "not_official_openai"
		return result
	}

	result.OpenAITierUpdated = true
	result.OpenAITier = inferOpenAITierFromHeaders(model, headers)
	result.OpenAITierReason = openAITierReason(result.OpenAITier, model, headers)
	return result
}

func isOfficialOpenAIAPI(endpoint *url.URL) bool {
	if endpoint == nil {
		return false
	}
	return strings.EqualFold(endpoint.Hostname(), "api.openai.com")
}

func inferOpenAITierFromHeaders(model string, headers http.Header) string {
	requests, ok := parseOpenAIRateLimitHeader(headers.Get("x-ratelimit-limit-requests"))
	if !ok {
		return ""
	}
	tokens, ok := parseOpenAIRateLimitHeader(headers.Get("x-ratelimit-limit-tokens"))
	if !ok {
		return ""
	}

	pair := openAIRateLimitPair{requests: requests, tokens: tokens}
	if family := openAIModelRateLimitFamily(model); family != "" {
		return inferOpenAITierFromFamilyLimitPair(family, pair)
	}

	return inferOpenAITierFromKnownLimitPair(pair)
}

func openAITierReason(tier, model string, headers http.Header) string {
	if tier != "" {
		return "inferred"
	}
	if strings.TrimSpace(headers.Get("x-ratelimit-limit-requests")) == "" {
		return "missing_requests_header"
	}
	if strings.TrimSpace(headers.Get("x-ratelimit-limit-tokens")) == "" {
		return "missing_tokens_header"
	}
	if _, ok := parseOpenAIRateLimitHeader(headers.Get("x-ratelimit-limit-requests")); !ok {
		return "invalid_requests_header"
	}
	if _, ok := parseOpenAIRateLimitHeader(headers.Get("x-ratelimit-limit-tokens")); !ok {
		return "invalid_tokens_header"
	}
	if openAIModelRateLimitFamily(model) == "" {
		return "unknown_model"
	}
	return "unmapped_limits"
}

func openAIModelRateLimitFamily(model string) string {
	model = strings.ToLower(strings.TrimSpace(model))

	switch {
	case strings.HasPrefix(model, "gpt-4.1-mini"),
		strings.HasPrefix(model, "gpt-4.1-nano"),
		strings.HasPrefix(model, "gpt-4o-mini"):
		return "gpt-4.1-small"
	case strings.HasPrefix(model, "gpt-4.1"):
		return "gpt-4.1"
	case strings.HasPrefix(model, "gpt-5.5-pro"):
		return "gpt-5.5-pro"
	case strings.HasPrefix(model, "gpt-5.5-mini"),
		strings.HasPrefix(model, "gpt-5.4-mini"),
		strings.HasPrefix(model, "gpt-5-mini"):
		return "gpt-5-mini"
	case strings.HasPrefix(model, "gpt-5.5-nano"),
		strings.HasPrefix(model, "gpt-5.4-nano"),
		strings.HasPrefix(model, "gpt-5-nano"):
		return "gpt-5-nano"
	case strings.HasPrefix(model, "gpt-5.5"),
		strings.HasPrefix(model, "gpt-5.4"),
		strings.HasPrefix(model, "gpt-5"):
		return "gpt-5"
	default:
		return ""
	}
}

func inferOpenAITierFromFamilyLimitPair(family string, pair openAIRateLimitPair) string {
	tiers, ok := openAIRateLimitTiersByFamily[family]
	if !ok {
		return ""
	}
	if tier := tiers[pair]; tier != "" {
		return tier
	}

	bestRank := 0
	for knownPair, tier := range tiers {
		if knownPair.requests > pair.requests || knownPair.tokens > pair.tokens {
			continue
		}
		if rank := openAITierRank(tier); rank > bestRank {
			bestRank = rank
		}
	}
	if bestRank == 0 {
		return ""
	}
	return "T" + strconv.Itoa(bestRank)
}

func inferOpenAITierFromKnownLimitPair(pair openAIRateLimitPair) string {
	var inferredTier string
	for _, tiers := range openAIRateLimitTiersByFamily {
		tier, ok := tiers[pair]
		if !ok {
			continue
		}
		if inferredTier != "" && inferredTier != tier {
			return ""
		}
		inferredTier = tier
	}
	return inferredTier
}

func openAITierRank(tier string) int {
	if len(tier) != 2 || tier[0] != 'T' {
		return 0
	}
	rank, err := strconv.Atoi(tier[1:])
	if err != nil || rank < 1 || rank > 5 {
		return 0
	}
	return rank
}

func parseOpenAIRateLimitHeader(value string) (int64, bool) {
	value = strings.TrimSpace(value)
	value = strings.ReplaceAll(value, ",", "")
	if value == "" {
		return 0, false
	}

	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil || parsed <= 0 {
		return 0, false
	}
	return parsed, true
}
