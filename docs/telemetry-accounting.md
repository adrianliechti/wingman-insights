# Usage accounting and chat activity

Gateway (`wingman`) provider calls are the accounting authority for model tokens
and cost. Insights identifies their instrumentation scope as
`github.com/adrianliechti/wingman`, independently of the configurable service name.
Gateway provider instrumentation reports the configured upstream model ID rather
than the public deployment alias so catalog pricing can resolve it.

Browser (`wingman-chat`) model observations retain their original attributes and
trace metadata. Their materialized token/cost counters are zero; their model
metrics use `wingman.chat.*` rather than gateway `gen_ai.*` names. Category/risk
scores (`wingman.classification.score`), tool spans, MCP server/tool metadata,
and execution-failure metrics remain stored. The Operations tool table includes
the MCP server ID. The browser telemetry proxy stamps a trusted `chat` source on
resources, preventing client-supplied instrumentation scopes from acquiring
accounting authority.

Model requests explicitly propagate their current browser model span as
`traceparent`, connecting gateway provider spans to the browser trace even after
asynchronous tool execution. Tool calls inherit their explicit parent context;
unrelated concurrent runs stay isolated.

Classification scores include the opaque saved chat ID as `gen_ai.conversation.id`.
Insights uses it to calculate classified prompts per conversation within the
selected period: matched category counts divided by distinct conversations with
those counts. Observations without an ID, risk-only conversations, and categories
below threshold are excluded from both sides of that average. Overall prompt and
risk totals still include observations without IDs. Historical records without
IDs cannot be correlated retroactively.

Chat IDs are not sent in provider request headers or attached to model-usage
metrics. Gateway accounting continues to use provider calls independently of
classification correlation. Topic shifts are not reported.

User authorization filters apply independently of trace correlation. Trace IDs
are not credentials and do not grant access to other users' telemetry.

On startup, recognized historical browser spans have materialized usage/cost
cleared while raw attributes remain; legacy browser `gen_ai.*` metrics are moved
to the `wingman.chat.*` namespace. Historical records without a conversation ID
cannot be linked retroactively. Equal independent provider calls both count, regardless of token equality.

Deploy all three changes together, or deploy Insights first: it recognizes the
existing gateway scope and separates older browser exports on ingestion. No
production database has been changed by the implementation or tests. Classification retry deduplication and delivery are described below.


## Classification delivery and server batching

Accepted classifications use a dedicated browser OTel meter provider and delta
HTTP exporter. Recording starts an asynchronous flush immediately, independently
of the ordinary 60-second metric interval. Flushes are serialized; observations
arriving during delivery trigger another flush. A hidden-page visibility change
also flushes. The installed OTel fetch transport uses keepalive for small requests,
subject to its size/concurrency limits. Cancelled and superseded runs retain the
existing browser acceptance guard and do not report their rejected results.

All category/risk observations for one accepted result share the opaque UUID
`wingman.classification.event_id`. Thresholds, stable category/risk IDs,
conversation IDs, and the existing no-content/no-client-identity policy remain.
No result text, prompts, or rule descriptions are exported.

With `INSIGHTS_ENDPOINT` configured, the chat proxy sends enriched metrics
synchronously to Insights and returns its acknowledgement, including retryable
503 and `Retry-After`. The primary collector gets a separate bounded best-effort
copy. Only transport headers are forwarded to Insights; identity is already in
the enriched payload. Trace ingestion and its best-effort Insights copy are
unchanged. Without `INSIGHTS_ENDPOINT`, the collector remains the primary target.

Insights acknowledges a valid export after admitting its supported datapoints to
an in-memory queue. One metric writer combines users' exports in a database
transaction every 5 seconds, or at 1,000 queued datapoints or 4 MiB of queued OTLP
protobuf payload, whichever comes first. The queue admits at most 10,000 datapoints
or 32 MiB, counting in-flight writes and retries. Over-limit admissions receive
HTTP 503 with `Retry-After: 1`. Unsupported metrics retain their existing ignore
behavior. Failed writes keep the entire batch and retry once per second; new
admissions remain bounded while the writer is failing.

Classification event IDs are claimed in a persistent `classification_events`
ledger in the same transaction as the observations. Retries insert or skip all
observations for an event together, including retries combined in a single batch.
Older clients without event IDs remain additive. The ledger is pruned using the
same telemetry timestamps and retention cutoff. Other metrics retain their
existing additive semantics and are not deduplicated. On shutdown, HTTP admission
stops, then the queue drains before DuckDB closes, with a 15-second drain deadline.

Queue depth is exposed as `insights_ingest_queued_datapoints` and
`insights_ingest_queued_bytes` at `GET /metrics`. Counters expose write failures,
rejected admissions, and committed datapoints. Write failures, rejected exports,
and any shutdown abandonment are logged without classification or user content.

Configure only **one route from each producer into Insights**. When direct
`INSIGHTS_ENDPOINT` delivery is enabled, the collector must not fan that producer's
metrics or traces back into Insights. Classification deduplication does not make
duplicate routes safe for usage, tool, or older-client metrics. Deploy Insights
before the chat changes so the event ledger and queue are available first.

Under normal load, a classification received by Insights appears once in queries
within five seconds, plus transaction/query time; dashboard refresh adds its own
display delay. No per-browser-event database transaction is required. This protects
already-started small exports against normal tab closure. It does not guarantee
delivery offline, after browser process termination, beyond keepalive limits, or
after exporter retries are exhausted. A server crash can lose acknowledged but
uncommitted data; durable buffering would be needed to remove that limitation.
