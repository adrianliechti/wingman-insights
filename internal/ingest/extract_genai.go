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
		rows = append(rows, store.GenAIMetricRow{
			ReceivedAt:  now,
			Time:        ts,
			ServiceName: serviceName,
			// service.peer.name is the calling app (the gateway stamps it on metric
			// data points); service_name above is the gateway's own resource name.
			// Falls back to service_name when no peer was stamped (non-OIDC auth or
			// a non-Entra app) — see appID.
			AppID:         appID(attrs, serviceName),
			MetricName:    m.Name,
			OperationName: getStringAttr(attrs, "gen_ai.operation.name"),
			ProviderName:  getStringAttr(attrs, "gen_ai.provider.name"),
			RequestModel:  getStringAttr(attrs, "gen_ai.request.model"),
			TokenType:     getStringAttr(attrs, "gen_ai.token.type"),
			ErrorType:     getStringAttr(attrs, "error.type"),
			EndUserID:     getStringAttr(attrs, "user.id"),
			EndUserEmail:  getStringAttr(attrs, "user.email"),
			// gen_ai.conversation.id is the semconv-standard correlation id;
			// session.id is a general fallback.
			SessionID:  firstStringAttr(attrs, "gen_ai.conversation.id", "session.id"),
			Count:      count,
			Sum:        sum,
			Attributes: attrsToMap(attrs),
		})
	}
	iterateDataPoints(m, processDP)
	return rows
}
