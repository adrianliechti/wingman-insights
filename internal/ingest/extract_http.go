package ingest

import (
	"strings"
	"time"

	"insights/internal/store"

	common "go.opentelemetry.io/proto/otlp/common/v1"
	metrics "go.opentelemetry.io/proto/otlp/metrics/v1"
)

func extractHTTP(m *metrics.Metric, serviceName string, now time.Time) []store.HTTPMetricRow {
	var rows []store.HTTPMetricRow
	direction := "server"
	if strings.HasPrefix(m.Name, "http.client.") {
		direction = "client"
	}

	processDP := func(attrs []*common.KeyValue, ts time.Time, count int64, sum, min, max float64) {
		rows = append(rows, store.HTTPMetricRow{
			ReceivedAt:  now,
			Time:        ts,
			ServiceName: serviceName,
			MetricName:  m.Name,
			Direction:   direction,
			Method:      getStringAttr(attrs, "http.request.method"),
			Route:       getStringAttr(attrs, "http.route"),
			StatusCode:  int(getIntAttr(attrs, "http.response.status_code")),
			ErrorType:   getStringAttr(attrs, "error.type"),
			// The gateway stamps the calling app (service.peer.name) and end user
			// (user.id / user.email) onto http.server metric data points via the
			// otelhttp labeler, so the App and User dashboard filters narrow these
			// rows. app_id falls back to service_name when no peer was stamped — see
			// appID, matching genai_metrics.
			AppID:      appID(attrs, serviceName),
			UserID:     getStringAttr(attrs, "user.id"),
			UserEmail:  getStringAttr(attrs, "user.email"),
			Count:      count,
			Sum:        sum,
			Attributes: attrsToMap(attrs),
		})
	}
	iterateDataPoints(m, processDP)
	return rows
}
