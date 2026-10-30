# Usage accounting and chat activity

Gateway (`wingman`) provider calls are the accounting authority for model tokens
and cost. Insights identifies their instrumentation scope as
`github.com/adrianliechti/wingman`, independently of the configurable service name.
Gateway provider instrumentation reports the configured upstream model ID rather
than the public deployment alias so catalog pricing can resolve it.

Browser (`wingman-chat`) model observations retain their original attributes and
trace metadata. Their materialized token/cost counters are zero; their model
metrics use `wingman.chat.*` rather than gateway `gen_ai.*` names. Category/risk
scores (`wingman.classification.score`), tool spans, and MCP server/tool metadata
remain stored. The unused browser execution-failure metric has been removed from
new chat clients; historical rows may remain until retention removes them. The Operations tool table includes
the MCP server ID. The browser telemetry proxy stamps a trusted `chat` source on
resources, preventing client-supplied instrumentation scopes from acquiring
accounting authority.

Model requests explicitly propagate their current browser model span as
`traceparent`, connecting gateway provider spans to the browser trace even after
asynchronous tool execution. Tool calls inherit their explicit parent context;
unrelated concurrent runs stay isolated.

## Customers activity

Customers → Activity aggregates stored execution spans over the selected range
and dashboard filters. It counts `invoke_agent`, the chat's `agent` tool and its
hashed MCP `@agent` wrapper as agent runs; `load_skill` as skill loads; other MCP
tools as connector calls; and remaining tools separately. Model and loop-wrapper
spans are excluded. Repeated exports of the same trace/span ID count once. Calls
with no explicit successful or failed outcome remain unreported, rather than
being assumed successful.

Counts describe observed usage rather than an inventory of installed capabilities.
The view reports coverage of `gen_ai.agent.id`, `user.id`/`user.email`, and the
standard `gen_ai.skill.name` and custom `wingman.plugin.id` attributes.
The skill name is the qualified catalog name (for example, `office:reports`).
Legacy `wingman.skill.name` and `wingman.skill.id` remain query fallbacks.
These skill/plugin attributes are read when supplied;
the dashboard does not infer them from tool arguments or enable their emission.
Skill loads without identity remain grouped as “Unidentified skill”. The
synthetic MCP agent wrapper is an agent run, not another remote connector call.

The activity summary distinguishes executions from distinct reported identities,
including configured agents separately from delegated/built-in agents,
with headline totals for MCP tool calls and built-in tool calls beside distinct
custom agents used. These execution totals match their chart/table scope, not
the number of configured servers or tools.
The agent
card shows distinct non-plugin configured agents and their run count, matching
the Custom agents row. Delegated chat helpers and legacy runs without a confirmed
configured kind are excluded. The history chart counts custom-agent runs
in each interval, including zero-use intervals. Repeated runs of one agent count
separately; the chart's run total matches the card's run count and the custom row.
Agent bars stack First observed runs and Other runs; the full height is total
runs. Other runs includes repeat runs and runs without an agent ID, so missing
identity is not assumed to mean new or returning. The first-observed segment is based on
the earliest identified custom-agent run across all retained history before date
and dashboard filters are applied. The earliest run itself must match the
selected view. Missing IDs cannot contribute; changing the range or filters does
not reset an existing agent's first-observed date. Creation events are not
recorded by the current client, so first observed is not labeled as creation.
All views use stacked bars per interval, with the same bucket layout as
Personal: 24 hourly bars for 24h, then one local calendar-day
bar per selected day (3/7/14/30). The current partial interval is included and
quiet intervals remain visible. Activity summaries and tables use the same
aligned window as the chart. Hourly execution points are aggregated by local
date for daily bars so daylight-saving changes do not shift recorded usage to
another day. Only the selected chart category is fetched.
The chart uses the same segmented
switch as FinOps: Agents, MCP, Plugins, and Built-in tools. The standalone
Skills chart and details are omitted; the distinct-skills headline is also omitted. The other views count skill loads, MCP tool calls, explicitly
plugin-attributed executions, and built-in tool calls per interval, using the same
deduplication and filters as their activity tables. Missing intervals are zero.
Plugin counts overlap other views and are never added to those counts.
MCP and Plugin chart views split executions into a series per observed server or
plugin. Server series use stable server identity and plugin ownership with the
latest display name; duplicate display names are disambiguated. A single-series
legend remains visible so the used server or plugin is named. Each observed
entity has zero-filled intervals, and empty filters return no entity series.
The Built-in tools chart likewise splits calls by tool, combining calls to the
same tool across plugin ownership. These three views use stacked bars: each bar’s full height represents all calls in the
interval. Hovering shows each entity's contribution and the interval total. Incomplete edge intervals are dimmed.
Missing IDs contribute to run counts but not distinct-agent counts. Stable
IDs keep renamed agents in one group; skill IDs and explicit plugin ownership
keep equally named skills in different plugins distinct. User reach resolves
directory aliases before counting and excludes known application principals.
Unknown user identity remains unknown. Plugin rows are an overlapping attribution
view, not another category to add to capability totals. Distinct counts are
labeled as reported identities; missing identity data is explained in the data
coverage details. All activity queries use
the same date range, admin authorization and dashboard filters.

New chat clients emit these values on existing spans without capturing payloads.
The Activity table omits individual agent rows and its filters contain no Agents
option. Agent identities remain stored for aggregate counts and trace analysis;
user-defined agents from individual computers are not listed as shared capabilities.
Known frontend tools (script execution, image creation, questions, file operations,
repository access and skill management/resource reads) appear under Built-in tools.
Other tools is the fallback for unknown names; names merely sharing a built-in
prefix are not assumed to be built-ins. This fallback remains stored but is
omitted from the table and filters. Skill rows are listed only with explicit
plugin attribution; personal, unidentified and other non-plugin skills remain
stored and included in aggregate counts but are not listed. Plugin skills appear
inside expandable plugin rows, with their own load counts, instead
of a separate filter or top-level skill rows. The chart and usage table share one card and category switch. Selecting MCP,
Plugins, or Built-in tools updates both together. Agents stays chart-only.
Plugin skill details remain nested beneath their owning plugin.
There is no separate table filter or All view. Custom agents and skills
are covered by the headline metrics and agent-history chart, not the usage table.
Trace inspection remains under Operations → Traces. Agent,
skill-load and MCP classification
takes precedence over the built-in name list.

MCP server rows likewise expand into the server's used tools and call counts.
The server parent aggregates raw deduplicated calls, so duration is weighted by
calls and users/conversations are counted distinctly across tools. It is not a
sum of per-tool user counts. Server IDs and plugin ownership determine child
membership; missing server identity is explicitly grouped as unidentified. The
MCP servers view provides the server breakdown for both standalone and plugin-owned
servers. Plugin totals overlap with their attributed activity in other views.

The custom-activity API retains two pooled rows: Custom agents combines
non-plugin configured-agent runs; Custom skills combines non-plugin skill loads
explicitly tagged `wingman.skill.source=personal`. Shipped skills (`builtin`),
plugin skills and older untyped skills are excluded from the personal total.
Custom rows keep distinct user reach and weighted duration, but return no private
names, IDs or trace references. The API includes zero-usage rows. All underlying
spans and identity attributes remain stored for authorized trace analysis.
Configured-agent run wrappers use `invoke_agent` with `wingman.agent.kind` and
`wingman.agent.outcome`. Delegation tools use `gen_ai.transfer.target.name` and
`gen_ai.transfer.mode=return_to_caller`, plus `wingman.transfer.target.id` and
`wingman.transfer.target.kind` for target identity. Their `gen_ai.agent.*` fields
identify the caller, not the target; their child model wrappers do not count again. Chat and
tool spans carry the original saved `gen_ai.conversation.id` across delegation.
Anonymous local sessions still have no user identity; authenticated producer/
proxy enrichment is required for per-user adoption. Existing traces retain
their missing metadata and cannot be backfilled by the dashboard.

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

## GenAI attribute compatibility

Activity queries prefer `gen_ai.skill.name` over legacy skill fields. Its qualified
name is already the skill identity, so plugin namespaces are not appended again.
Transfer tools are classified using `gen_ai.transfer.target.name` or
`gen_ai.transfer.mode`. Target ID/kind/name are kept separate from the executing
agent, including when target identity is absent. Older delegation attributes and
MCP wrapper names still work. MCP tool names prefer `gen_ai.tool.name`, then
legacy `mcp.tool.name`, then the materialized tool column. Resource reads retain
`gen_ai.skill.resource.name` in raw attributes without counting another skill load.

No new schema migration, data rewrite, or backfill is required: attributes are
stored as JSON and compatibility is applied at query time. Deploy Insights before
the updated chat client; existing and new spans can coexist. Existing startup
accounting migrations are unchanged. No live database is modified by this update.

`wingman.classification.score` and its dimensions are consumed by classification
queries and retry deduplication. The browser `wingman.chat.*` metrics participate
in general active-user and filter queries even though accounting queries use only
gateway `gen_ai.*` metrics. Keep these metrics. `wingman.execution.failures` and
`wingman.execution.language` have no dedicated Insights consumer, so that unused
instrumentation has been removed from script execution.
