# Insights MCP

Set `INSIGHTS_MCP_ENABLED=true` to serve a stateless Streamable HTTP MCP endpoint
at `/mcp`, or `<INSIGHTS_BASE_PATH>/mcp` when a base path is configured. It shares
the running service's store; no second process or database is required.

## Identity and access

Supply `Authorization: Bearer <JWT>` on **every** request. For this MVP, Insights
decodes the JWT payload and **does not verify its signature, issuer, audience, or
expiry**. Token trust is supplied by the caller/upstream integration. There is no
OAuth login/discovery flow or token issuance in this implementation. This is a
temporary trusted-token integration, not a standalone authentication boundary.
The `oid` claim identifies the caller, falling back to `sub` if `oid` is absent.
This value must match a telemetry user ID or a directory alias. No token or a
missing identity returns HTTP 401. Forwarded identity/group headers are ignored.
Existing dashboard and companion authentication remains separate, including its
existing startup configuration requirements.

If the token's `groups` array contains the exact value of `INSIGHTS_ADMIN_GROUP`,
the caller can query organization-wide data and use all six tools. If the
variable is unset, nobody receives admin access. Other callers see only
`get_usage`, `get_cost_breakdown`, and `get_prompt_sizes`; their user filter is always replaced by their
token identity, even if they request someone else's data. Grouping by department
or location still returns only their own costs. The dashboard's own development
auth flags do not by themselves grant MCP access or bypass the requirement for a
token; see [Local development](#local-development).

Configure your MCP client with this HTTP URL and bearer header. For example:

```json
{
  "mcpServers": {
    "insights": {
      "type": "http",
      "url": "https://insights.example.com/mcp",
      "headers": { "Authorization": "Bearer <token supplied securely at runtime>" }
    }
  }
}
```

Client configuration syntax varies; use its remote Streamable HTTP option. Use
HTTPS in deployment. Request bodies are limited to 1 MiB. Keep tokens out of
checked-in configuration and logs.

## Browser clients and CORS

Same-origin browser requests and clients without an `Origin` header are
supported; cross-origin browser requests are rejected with 403 and no CORS
headers, so a web client on another origin cannot connect by default. Tools that
proxy through their own backend — including the MCP Inspector, whose browser UI
calls its own origin — are unaffected.

Set `INSIGHTS_MCP_ALLOWED_ORIGINS` to a comma-separated list of origins to admit
browser clients. Listed origins become trusted, preflight `OPTIONS` requests are
answered without a token, and responses expose `Mcp-Session-Id`,
`Mcp-Protocol-Version`, and `WWW-Authenticate` so client SDKs can read them.

```sh
INSIGHTS_MCP_ALLOWED_ORIGINS=https://client.example,https://other.example
```

The value `*` admits any origin and disables the cross-origin protection
altogether, logging a warning at startup. Because tokens are trusted without
verification, `*` means any website a user visits can call this endpoint with
whatever identity it chooses — only use it where the network is the real
boundary, and never together with `INSIGHTS_MCP_AUTH_DISABLED`, which would let
any page read organization-wide data anonymously.

## Local development

Set `INSIGHTS_MCP_AUTH_DISABLED=true` to accept unauthenticated callers as a
fixed identity instead of answering 401, so local clients need no token
plumbing. It is **development only** and off by default. Because it hands data
to anonymous callers, the server additionally requires `INSIGHTS_AUTH_DISABLED=true`
and exits at startup otherwise, so no single variable can open up a deployment.
Enabling it logs a warning naming the assumed identity.

The identity defaults to `dev`, matching the dashboard's dev user. Override it
with `INSIGHTS_MCP_DEV_USER` to impersonate a real telemetry user — useful
because `dev` usually has no recorded usage, so personal scope returns empty
data. `INSIGHTS_DEV_ADMIN=true` grants the anonymous caller admin scope and all
four tools, mirroring the dashboard.

A valid bearer token still takes precedence when one is sent, so scope and
admin behavior remain testable without toggling the flag:

```sh
INSIGHTS_MCP_ENABLED=true INSIGHTS_AUTH_DISABLED=true \
INSIGHTS_MCP_AUTH_DISABLED=true INSIGHTS_MCP_DEV_USER=dev@example.com \
INSIGHTS_DEV_ADMIN=true go run .
```

Note that the embedded SPA answers unmatched paths, so OAuth discovery probes
such as `/.well-known/oauth-authorization-server` return the dashboard HTML
rather than 404. Clients that react to a 401 by attempting OAuth discovery can
fail confusingly; supply a token (or enable this bypass) so the 401 never
occurs.

## Tools and filters

| Tool | What it returns | Options |
| --- | --- | --- |
| `get_usage` | Cost, tokens, provider-call counts, per-call averages, cache/reasoning shares, optional trends and period comparison | Optional `interval`: `auto`, `hour` or `day`; `compare_previous` or explicit `compare_from`/`compare_to` |
| `get_cost_breakdown` | Ranked costs, cache savings, token partitions, unpriced tokens | `group_by`: `model` (default), `application`, `department`, `location`, `user`; `limit` |
| `get_categories` | Winning category counts and fractional shares | Admin only; `group_by`: `category` (default) or `department`; `limit` |
| `get_category_trends` | Winning category counts per UTC time bucket | Admin only; `interval`: `auto` (default), `hour` or `day` |
| `get_adoption` | Distinct observed users, new/returning activity, provider-call counts, and active-day frequency | Admin only; `interval`: `auto`, `hour`, or `day`; `chart_metric` selects the view |
| `get_prompt_sizes` | Ordered input-size bins with call counts, costs, per-call cost, shares, and unpriced coverage | `chart_metric`: `requests`, `cost_usd`, `request_share_percent`, or `cost_share_percent` |

All inputs are described in each tool's JSON schema; there is no filter-list
tool. Optional `applications`, `users`, `departments`, and `locations` arrays
apply across the tools. Usage, cost, adoption and prompt-size tools additionally accept `providers` and
`models`. Values within a field are ORed, different fields are ANDed. Application
and user aliases resolve through the directory; department/location matching
requires directory data. Department matching follows the deployment's prefix
setting. Provider/model values match exact IDs. There is no fuzzy label lookup
or dynamic value enumeration; unknown values produce empty data. At most 100
nonempty values are accepted per filter.

Use RFC3339 `from` and `to` for inclusive range bounds. `to` defaults to now;
`from` defaults to 24 hours before `to`. The maximum window is 90 days. Invalid
or inverted ranges are rejected. Ranked results default to 20 rows, maximum 100.
The response includes effective filters, access scope, warnings, total row count,
and truncation. Totals/shares cover all matches before truncation. Trends are
limited to 2,000 points; use daily buckets or narrower filters/ranges if exceeded.
Queries have a 10-second deadline.

## Creating charts

Every tool returns an additional `chart` object alongside the existing `data`.
Use `chart.rows` directly as a table in the client's plotting library. The server
prepares data and chart hints; the client renders and styles the visualization.
There is no image generation dependency, remote chart service, or HTML execution.

| Chart field | Meaning |
| --- | --- |
| `title`, `type` | Suggested title and chart type: `bar`, `line`, or `stacked_bar` |
| `x_axis`, `y_axis` | Labels, types (`nominal`, `temporal`, `quantitative`), and units |
| `rows[].x`, `rows[].x_label` | Stable group ID or UTC timestamp, and its display label |
| `rows[].series`, `rows[].series_label` | Stable legend ID and readable label |
| `rows[].value` | Numeric plotted measure; `null` for unobserved temporal points |
| `includes_other` | Whether groups outside the largest `top_n` were combined |
| `missing_data`, `partial_buckets`, `notes` | Gap policy, partial interval flag, and qualifications to display with the chart |

Use IDs for grouping and labels for ticks/legends. Two users with identical names
remain distinct. Assign colors by stable series ID to keep legends consistent.
Values stay numeric, without currency signs or formatted token
suffixes. Use the axis unit to format them at rendering time. Bar charts should
start at zero; horizontal bars suit long model/user/department labels. Percentage
charts use **0 to 100**, while the original `data.rows[].share` remains a fraction.
For department percentages, render the suggested stacked bars on a 0–100 axis.

The optional `top_n` input defaults to 8 and accepts 1–20. It controls chart groups,
independently of the existing raw-data `limit`. All remaining groups are combined
as **Other (remaining)** so the plotted totals are preserved, even when raw data
rows are truncated. Department comparisons apply the limit to departments and
categories. Their Other department's percentages are calculated from pooled prompt
counts, not averages of department percentages. Trend series are selected across
the whole range, keeping their identity stable over time.

For trends, use `interval: "auto"`. It selects hourly or daily buckets with room
for the aligned chart grid within 2,000 points. Explicit `hour` and `day` remain
available. The grid includes every UTC bucket intersecting the inclusive query
range for each selected series. Missing observations are `null`, never invented
zeroes: keep line gaps and disable connecting missing points. Only observed
series appear; an empty query returns `rows: []`. `partial_buckets` flags range
edges that do not cover a full bucket. Do not compare a partial bucket to a full
one without accounting for that difference.

Examples of tool arguments for common charts:

```jsonc
// get_usage: token consumption by model over time
{ "interval": "auto", "chart_metric": "total_tokens", "top_n": 5 }

// get_cost_breakdown: department spending, as ranked bars
{ "group_by": "department", "chart_metric": "cost_usd", "top_n": 8 }

// get_cost_breakdown: cache savings by model
{ "group_by": "model", "chart_metric": "cache_savings_usd" }

// get_categories: each department's topic mix, as percentage stacked bars
{ "group_by": "department", "chart_metric": "share_percent", "top_n": 6 }

// get_category_trends: changes in topic mix over time
{ "interval": "auto", "chart_metric": "share_percent", "top_n": 5 }
```

The examples use comments to name the tool; supply only the JSON object as tool
arguments. `get_usage` without an interval retains its totals-only behavior; its
`total_tokens` chart shows disjoint Input/Output/Cached partitions. Cost breakdown
charts support `cost_usd` (default), `total_tokens`, and `cache_savings_usd`.
Usage charts also support `requests`, `cost_per_request_usd`,
`input_tokens_per_request`, `output_tokens_per_request`, `tokens_per_request`,
`cache_read_share_percent`, `reasoning_share_percent`, and `cache_savings_usd`.
Other groups pool the numerators and denominators for averages/rates, rather
than summing or averaging averages. Prompt-size and active-day distributions
keep their natural order and are not collapsed into Other.

Category charts support `prompts` (default) and `share_percent`. Category trend
percentages are normalized within each observed bucket, including Other.

Include the chart notes when presenting it. Costs remain estimates and may be
incomplete when some models are unpriced; a plotted zero does not establish free
usage. Classified prompt shares describe classified activity only. The original
data, authorization filters, and accounting rules are unchanged.

Example arguments:

```json
{ "from": "2026-10-01T00:00:00Z", "to": "2026-10-08T00:00:00Z", "group_by": "department" }
```

Use those arguments with `get_cost_breakdown` for department spend, or
`get_categories` for topic shares within each department. Token totals follow
the existing ledger: input + output + cached is the total; reasoning is a subset
of output. Costs and cache savings are USD estimates at stored catalog prices;
`priced=false` and `unpriced_tokens` preserve unknown-price coverage.

Category results include observed IDs and readable labels derived from those
IDs, not authoritative category descriptions. Counts use matched winning
categories only, exclude risk checks, and represent classified prompts rather
than all activity. Department shares use each department's classified prompt
total; an empty department means unassigned. Missing trend buckets are not
filled. No prompt content is returned and no costs are attributed to categories.

Operations, anomalies, risks, raw traces, arbitrary SQL, writes, and MCP
prompts/resources are outside this MVP.

## Answering user questions

The six tools cover the following workflows. Apply `from`/`to` and the same
filters to every call in a comparison. Admin-only views include departments
across the organization, categories, and adoption. Personal callers can compare
their own periods and inspect their own prompt sizes.

| Question | Tool and relevant arguments |
| --- | --- |
| How much have I spent each day? | `get_usage`: `interval=day`, `chart_metric=cost_usd` |
| Has spending increased since the previous period? | `get_usage`: `compare_previous=true`; compare the returned totals and changes |
| How does this calendar month compare with last month? | `get_usage`: explicit current `from`/`to` and `compare_from`/`compare_to` for last month's bounds |
| Which models account for most spending? | `get_cost_breakdown`: `group_by=model`, `chart_metric=cost_usd` |
| Which applications consume the most tokens? | `get_cost_breakdown`: `group_by=application`, `chart_metric=total_tokens` |
| How much estimated cost has caching saved? | `get_cost_breakdown`: `group_by=model`, `chart_metric=cache_savings_usd` |
| Which departments spend the most? | `get_cost_breakdown`: `group_by=department` |
| Is a department's spending high because of volume or model mix? | Filter that department; compare `get_usage` call/token statistics and `get_cost_breakdown` by model across both periods |
| What do people use AI for? | `get_categories`: counts or `chart_metric=share_percent` |
| How does topic mix differ across departments? | `get_categories`: `group_by=department`, `chart_metric=share_percent` |
| Which topics are becoming more common? | `get_category_trends`: `interval=auto`; compare both `prompts` and `share_percent`, since volume and mix answer different questions |
| Is spending growth accompanied by more calls or larger calls? | `get_usage`: `compare_previous=true`; inspect requests, input/output tokens per call, cache-read share and reasoning share; use the corresponding `chart_metric` for trends |
| Are people using AI regularly or just trying it? | `get_adoption`: `chart_metric=active_days` for frequency, `new_vs_returning_users` for activity over time |
| Are large prompts responsible for a disproportionate share of costs? | `get_prompt_sizes`: compare `request_share_percent` with `cost_share_percent` for the same bins |

### Usage statistics and comparisons

`get_usage.data.statistics` and each trend bucket's `statistics` include:

- `requests`: token-bearing provider spans, using the same accounting source as
  usage and cost. Browser observations and zero-token tool spans do not add calls.
- `input_tokens_per_request`: inclusive input, including cache read/creation.
  `output_tokens_per_request` includes reasoning. `tokens_per_request` counts
  input + output + cached once, using the existing disjoint ledger.
- `cost_per_request_usd`: known stored estimated cost divided by observed calls;
  it can be incomplete when models are unpriced.
- `cache_read_share_percent`: cache-read tokens / inclusive input × 100; cache
  creation is not a cache hit. `reasoning_share_percent`: reasoning / output × 100.
- `cache_read_tokens`, `cache_creation_tokens`, `unpriced_tokens`: coverage and
  cache details to qualify interpretations.

Undefined averages/shares are `null`, never zero or infinity. Existing totals,
token fields and raw bucket fields remain available.

`compare_previous=true` selects the immediately preceding equal-length window,
ending one nanosecond before current `from`, so boundary events are not counted
twice. Alternatively supply both `compare_from` and `compare_to` (RFC3339); do not
combine them with `compare_previous`. Each window is limited to 90 days.
Both windows use the caller's enforced identity and the same filters.

`data.comparison` contains previous-period bounds, cost/tokens/statistics,
`changes` for all supported usage metrics, and a two-period chart for the selected
measure. Each change includes current/previous values, absolute change and
percentage change. Percentage change from a zero baseline is `null`; an absolute
change in a percentage metric is expressed in percentage points. Unequal custom
windows are flagged: total changes are not normalized by duration.

These statistics identify observed contributors to spending changes; they do
not prove causality, quantify quality or predict savings from switching models.
Inspect model mix with a cost-breakdown call for each period rather than attributing
all changes in cost per call to token length.

### Adoption and prompt-size definitions

`get_adoption` deduplicates directory aliases and counts observed identities on
token-bearing provider calls. Anonymous calls and directory principals known to
be applications are excluded. Unresolved identity kinds can remain; these are
observed-user counts, not a count of licensed employees. New/returning status is
based on the first matching activity in retained history under the selected
filters, not registration or an account's lifetime history. Retention can erase
older activity. Window totals count distinct users; bucket counts are not additive.

`single_day_users` and `repeat_day_users` count users active on one or multiple
distinct UTC dates **within this window**. Neither establishes future abandonment
or productivity. `chart_metric=active_days` returns an ordered frequency chart;
`active_users` (default), `new_users`, `returning_users`, `new_vs_returning_users`,
and `requests` select time-series charts. No per-user records are exposed by this tool.

`get_prompt_sizes` returns all five ordered bins, with exact inclusive token
bounds (last upper bound is `null`), counts, costs, unpriced calls/tokens, per-call
cost and request/cost percentages. Sizes include cached input; output-only calls
are excluded. Empty bins are known zero counts; no matching calls produces an
empty chart and undefined shares. If all calls are unpriced, cost shares remain
`null`. Cost shares describe **known estimated spend** and can be incomplete.

No category costs, answer-quality scores, licensed-user adoption rates, time
savings, or ROI are inferred. Operations and anomalies remain outside these tools.

## Tests

`go test ./internal/mcp` exercises SDK discovery and tool calls over HTTP,
per-request identity scoping, admin access, filters, stored accounting semantics,
category counts/shares/trends, chart totals and units, Other aggregation,
percentage denominators, duplicate display labels, null gaps, automatic
intervals, invalid inputs, limits, empty data, cancellation, and generic error
responses. Further HTTP tests cover comparison boundaries, zero baselines,
personal scope in both periods, pooled rate denominators, observed-user identity
deduplication, new/returning history, application/anonymous exclusions, prompt-size
bin boundaries, cost coverage and empty distributions.
Run `go test ./...` for backend regression coverage
after building the embedded web assets (`npm ci && npm run build` in `web`).
category counts/shares/trends, invalid inputs, limits, empty data, cancellation,
the development auth bypass, the CORS allowlist, and generic error responses. Run
`go test ./...` for backend regression coverage after building the embedded web
assets (`npm ci && npm run build` in `web`).
