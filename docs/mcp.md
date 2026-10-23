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
the caller can query organization-wide data and use all four tools. If the
variable is unset, nobody receives admin access. Other callers see only
`get_usage` and `get_cost_breakdown`; their user filter is always replaced by their
token identity, even if they request someone else's data. Grouping by department
or location still returns only their own costs. Development auth flags do not
grant MCP access or bypass the requirement for a token.

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
HTTPS in deployment. Same-origin browser requests and clients without an Origin
header are supported; cross-origin browser requests are rejected. Request bodies
are limited to 1 MiB. Keep tokens out of checked-in configuration and logs.

## Tools and filters

| Tool | What it returns | Options |
| --- | --- | --- |
| `get_usage` | Estimated USD cost, disjoint token totals, pricing coverage, optional per-model trends | Optional `interval`: `auto`, `hour` or `day`; chart metric: `cost_usd` or `total_tokens` |
| `get_cost_breakdown` | Ranked costs, cache savings, token partitions, unpriced tokens | `group_by`: `model` (default), `application`, `department`, `location`, `user`; `limit` |
| `get_categories` | Winning category counts and fractional shares | Admin only; `group_by`: `category` (default) or `department`; `limit` |
| `get_category_trends` | Winning category counts per UTC time bucket | Admin only; `interval`: `auto` (default), `hour` or `day` |

All inputs are described in each tool's JSON schema; there is no filter-list
tool. Optional `applications`, `users`, `departments`, and `locations` arrays
apply across the tools. Usage/cost tools additionally accept `providers` and
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

## Tests

`go test ./internal/mcp` exercises SDK discovery and tool calls over HTTP,
per-request identity scoping, admin access, filters, stored accounting semantics,
category counts/shares/trends, chart totals and units, Other aggregation,
percentage denominators, duplicate display labels, null gaps, automatic
intervals, invalid inputs, limits, empty data, cancellation, and generic error
responses. Run `go test ./...` for backend regression coverage
after building the embedded web assets (`npm ci && npm run build` in `web`).
