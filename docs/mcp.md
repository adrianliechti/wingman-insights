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
| `get_usage` | Estimated USD cost, disjoint token totals, pricing coverage, optional per-model trends | Optional `interval`: `hour` or `day` |
| `get_cost_breakdown` | Ranked costs, cache savings, token partitions, unpriced tokens | `group_by`: `model` (default), `application`, `department`, `location`, `user`; `limit` |
| `get_categories` | Winning category counts and fractional shares | Admin only; `group_by`: `category` (default) or `department`; `limit` |
| `get_category_trends` | Winning category counts per UTC time bucket | Admin only; `interval`: `hour` (default) or `day` |

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
category counts/shares/trends, invalid inputs, limits, empty data, cancellation,
and generic error responses. Run `go test ./...` for backend regression coverage
after building the embedded web assets (`npm ci && npm run build` in `web`).
