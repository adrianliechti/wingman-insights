package ingest

import (
	"time"

	"insights/internal/store"

	common "go.opentelemetry.io/proto/otlp/common/v1"
	metrics "go.opentelemetry.io/proto/otlp/metrics/v1"
)

func extractGenAI(m *metrics.Metric, serviceName string, now time.Time) []store.GenAIMetricRow {
	var rows []store.GenAIMetricRow
	processDP := func(attrs []*common.KeyValue, ts time.Time, count int64, sum, min, max float64) {
		provider := getStringAttr(attrs, "gen_ai.system")
		if provider == "" {
			provider = getStringAttr(attrs, "gen_ai.provider.name")
		}
		// wingman emits user.id/user.email; enduser.* is the older semconv name.
		userID := getStringAttr(attrs, "user.id")
		if userID == "" {
			userID = getStringAttr(attrs, "enduser.id")
		}
		userEmail := getStringAttr(attrs, "user.email")
		if userEmail == "" {
			userEmail = getStringAttr(attrs, "enduser.email")
		}
		sessionID := getStringAttr(attrs, "session.id")
		if sessionID == "" {
			sessionID = getStringAttr(attrs, "gen_ai.conversation.id")
		}
		rows = append(rows, store.GenAIMetricRow{
			ReceivedAt:    now,
			Time:          ts,
			ServiceName:   serviceName,
			MetricName:    m.Name,
			OperationName: getStringAttr(attrs, "gen_ai.operation.name"),
			ProviderName:  provider,
			RequestModel:  getStringAttr(attrs, "gen_ai.request.model"),
			ResponseModel: getStringAttr(attrs, "gen_ai.response.model"),
			TokenType:     getStringAttr(attrs, "gen_ai.token.type"),
			ServerAddress: getStringAttr(attrs, "server.address"),
			ErrorType:     getStringAttr(attrs, "error.type"),
			EndUserID:     userID,
			EndUserEmail:  userEmail,
			SessionID:     sessionID,
			Count:         count,
			Sum:           sum,
			MinVal:        min,
			MaxVal:        max,
			Attributes:    attrsToMap(attrs),
		})
	}
	iterateDataPoints(m, processDP)
	return rows
}
