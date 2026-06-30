package ingest

import (
	"io"
	"net/http"

	collogs "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	"google.golang.org/protobuf/proto"
)

// DiscardLogs accepts OTLP log exports and drops them. Senders like wingman
// export logs to the same OTLP endpoint as metrics and traces; without this
// route every export would fail with a 404 and clutter the senders' logs
// with retry errors.
func DiscardLogs() http.Handler {
	return discardHandler(&collogs.ExportLogsServiceResponse{})
}

func discardHandler(resp proto.Message) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, io.LimitReader(r.Body, otlpBodyLimit))
		writeOTLP(w, r.Header.Get("Content-Type"), resp)
	})
}
