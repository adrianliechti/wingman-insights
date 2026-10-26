# Insights MCP

Set `INSIGHTS_MCP_ENABLED=true` to serve a stateless Streamable HTTP MCP endpoint
at `/mcp`, or `<INSIGHTS_BASE_PATH>/mcp` when a base path is configured. It shares
the running service's store; no second process or database is required.

## Identity and access

Supply `Authorization: Bearer <token>` on **every** request. Insights verifies the
token exactly like the companion API: the signature, issuer, audience, and expiry
are checked against `COMPANION_API_ISSUER` and `COMPANION_API_AUDIENCE`. There is
no OAuth login/discovery flow or token issuance in this implementation; clients
obtain a token for that audience from the identity provider themselves.
The `oid` claim identifies the caller, falling back to `sub` if `oid` is absent.
This value must match a telemetry user ID or a directory alias. A missing, invalid,
or expired token, or one without an identity, returns HTTP 401. The
`X-Forwarded-User`/`-Groups` headers are ignored. With `INSIGHTS_AUTH_DISABLED=true` no token
can be verified, so every request is refused unless the development bypass below
is enabled.

MCP is admin-only, with the same rule as the dashboard's organization-wide views:
the token's `groups` array must contain the exact value of `INSIGHTS_ADMIN_GROUP`.
Any other valid token is refused with HTTP 403, and if the variable is unset nobody
is admitted (a startup warning says so). Admitted callers see organization-wide
data; the `users` filter selects users but is not a permission boundary. The
dashboard's own development auth flags do not by themselves grant MCP access or
bypass the requirement for a token; see [Local development](#local-development).

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
because `dev` usually has no recorded usage. MCP requires admin, so
`INSIGHTS_DEV_ADMIN=true` is also needed; without it the anonymous caller is
refused with 403 (a startup warning says so), mirroring the dashboard.

A valid bearer token still takes precedence when one is sent, so admin
behavior remains testable without toggling the flag:

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
| `get_usage` | Cost, tokens, provider-call counts, per-call averages, cache/reasoning percentages, trends by model, ranked groups, prompt-size bins, and user adoption | `interval`: `auto`, `hour` or `day` for a trend; `group_by`: `model`, `application`, `department`, `location`, `user` (ranked by cost, with `limit`) or `prompt_size`; `view`: `usage` (default) or `adoption` |
| `get_categories` | Classified prompt counts and shares per category, optionally per department, or counts over time | `group_by`: `category` (default) or `department`; `limit`; `interval`: `auto`, `hour` or `day` |

The server and both tools advertise the Lucide `receipt` icon as embedded SVG
data URIs, in light- and dark-theme variants.

`group_by` and `interval` cannot be combined, and `view=adoption` cannot be
combined with `group_by`. `auto` selects hourly buckets for windows up to 48
hours, otherwise daily; buckets align to UTC.

All inputs are described in each tool's JSON schema; there is no filter-list
tool. Optional `applications`, `users`, `departments`, and `locations` arrays
apply to both tools. `get_usage` additionally accepts `providers` and
`models`. Values within a field are ORed, different fields are ANDed. Application
and user aliases resolve through the directory; department/location matching
requires directory data. Department matching follows the deployment's prefix
setting. Provider/model values match exact IDs. There is no fuzzy label lookup
or dynamic value enumeration; unknown values produce empty data. At most 100
nonempty values are accepted per filter.

Use RFC3339 `from` and `to` for inclusive range bounds. `to` defaults to now
(whole seconds); `from` defaults to 24 hours before `to`. The maximum window is
90 days. Invalid or inverted ranges are rejected. Ranked results default to 20
rows, maximum 100. Trends are limited to 2,000 points; use daily buckets or
narrower filters/ranges if exceeded. Queries have a 10-second deadline.

## Response format

Every tool returns the same envelope, as structured content and as JSON text.
Each tool declares typed `inputSchema` and `outputSchema` (covering all views); there are no
boolean subschemas, `null` values or `type` arrays, which some MCP clients reject.

```json
{
  "query": { "from": "…", "to": "…", "group_by": "user", "filters": { "users": ["alice"] } },
  "warnings": ["Some usage has no catalog price; costs are incomplete (see unpriced_tokens)."],
  "data": { "totals": { … }, "groups": [ … ], "total_groups": 2 }
}
```

`query` echoes the effective range, the selected `view`/`group_by`/`interval`,
and filters. `warnings` contains only conditions that apply to this result:
unpriced usage, no matching data, or a first/last bucket covering only part of its
interval. Show them with the answer. Definitions that always apply are in the tool
descriptions rather than repeated in each response.

Conventions used in all views:

- Costs are `*_usd`, rounded to 6 decimals. Percentages are `*_percent` from 0 to
  100; they and per-call averages are rounded to 2 decimals.
- Undefined ratios (zero denominator) are **omitted**, never reported as zero.
- `data` contains only the fields of the selected view. Top-level lists,
  `total_*` counts and `truncated` are omitted when empty, zero or false
  (`warnings` reports when nothing matched). Trend `points` exist only for
  buckets with data; a missing bucket is absent telemetry, not a known zero.
- Times are RFC3339 UTC bucket starts.

### `get_usage` data

| Arguments | `data` |
| --- | --- |
| *(none)* | `{totals}` |
| `interval` | `{totals, points: [{time, model, …usage}]}` |
| `group_by=model\|application\|department\|location\|user` | `{totals, groups: [{id, name?, department?, location?, …tokens}], total_groups, truncated}` |
| `group_by=prompt_size` | `{prompt_sizes: {totals: {requests, cost_usd, unpriced_requests, unpriced_tokens}, bins: [{label, min_input_tokens, max_input_tokens, requests, cost_usd, unpriced_requests, request_percent, cost_percent, cost_per_request_usd}]}}` |
| `view=adoption` | `{adoption: {totals: {active_users, new_users, returning_users, single_day_users, repeat_day_users, requests}, points: [{time, active_users, new_users, returning_users, requests}], frequency: [{active_days, users}]}}` |

Usage figures (`totals`, each trend point) contain:

- `requests`: token-bearing provider spans, using the same accounting source as
  usage and cost. Browser observations and zero-token tool spans do not add calls.
- `cost_usd`, `cache_savings_usd`: estimates at stored catalog prices, not
  provider invoices. `unpriced_tokens > 0` means cost is incomplete.
- `input_tokens` includes `cache_read_tokens` and `cache_creation_tokens`;
  `output_tokens` includes `reasoning_tokens`; `total_tokens = input_tokens + output_tokens`.
- `cost_per_request_usd`, `input_tokens_per_request`, `output_tokens_per_request`.
- `cache_read_percent`: cache reads / input × 100; cache creation is not a cache
  hit. `reasoning_percent`: reasoning / output × 100.

Groups carry the same token and cost fields but no request counts or per-call
averages. Group IDs: `provider/model` for models, the application ID, the
canonical user ID (with resolved `name`, `department`, `location`), or the
department/location value. An empty `id` means unassigned. Groups are ranked by
cost; `totals` cover all groups, including those beyond `limit`.

### `get_categories` data

| Arguments | `data` |
| --- | --- |
| *(none)* | `{total_prompts, categories: [{category, prompts, share_percent}], total_categories, truncated}` |
| `group_by=department` | as above, each row also has `department`; `share_percent` is within that department |
| `interval` | `{total_prompts, points: [{time, category, prompts, share_percent}]}`, `share_percent` within each bucket |

Category IDs come from telemetry, not authoritative descriptions. Counts use
matched winning categories only, exclude risk checks, and represent classified
prompts rather than all activity. An empty department means unassigned. No prompt
content is returned and no costs are attributed to categories.

Operations, anomalies, risks, raw traces, arbitrary SQL, writes, and MCP
prompts/resources are outside this MVP.

## Answering user questions

The two tools cover the following workflows. Apply `from`/`to` and the same
filters to every call in a comparison.

| Question | Tool and relevant arguments |
| --- | --- |
| How much has the organization spent each day? | `get_usage`: `interval=day`; sum `cost_usd` of the points per day |
| Has spending increased since the previous period? | `get_usage`: call once per period with the same filters; compare `totals` |
| How does this calendar month compare with last month? | `get_usage`: call once with this month's `from`/`to` and once with last month's |
| Which models account for most spending? | `get_usage`: `group_by=model` |
| Which applications consume the most tokens? | `get_usage`: `group_by=application`; compare `total_tokens` |
| How much estimated cost has caching saved? | `get_usage`: `cache_savings_usd` in `totals`, or `group_by=model` per model |
| Which departments spend the most? | `get_usage`: `group_by=department` |
| Is a department's spending high because of volume or model mix? | Filter that department; compare `get_usage` totals and `get_usage` with `group_by=model` across both periods |
| What do people use AI for? | `get_categories` |
| How does topic mix differ across departments? | `get_categories`: `group_by=department` |
| Which topics are becoming more common? | `get_categories`: `interval=auto`; compare both `prompts` and `share_percent`, since volume and mix answer different questions |
| Is spending growth accompanied by more calls or larger calls? | `get_usage`: call once per period; compare requests, per-call tokens, `cache_read_percent` and `reasoning_percent`; add `interval` for trends |
| Are people using AI regularly or just trying it? | `get_usage`: `view=adoption`; `frequency` for active days, `points` for new versus returning users over time |
| Are large prompts responsible for a disproportionate share of costs? | `get_usage`: `group_by=prompt_size`; compare `request_percent` with `cost_percent` for the same bins |

Percentage changes from a zero baseline are undefined, not zero. These figures
identify observed contributors to spending changes; they do not prove causality,
quantify quality or predict savings from switching models. Inspect model mix with
a `group_by=model` call for each period rather than attributing all changes in
cost per call to token length.

### Adoption and prompt-size definitions

`view=adoption` deduplicates directory aliases and counts observed identities on
token-bearing provider calls. Anonymous calls and directory principals known to
be applications are excluded. Unresolved identity kinds can remain; these are
observed-user counts, not a count of licensed employees. New/returning status is
based on the first matching activity in retained history under the selected
filters, not registration or an account's lifetime history. Retention can erase
older activity. Window totals count distinct users; bucket counts are not additive.
`interval` defaults to `auto`.

`single_day_users` and `repeat_day_users` count users active on one or multiple
distinct UTC dates **within this window**. Neither establishes future abandonment
or productivity. No per-user records are exposed by this view.

`group_by=prompt_size` returns all five ordered bins, with exact inclusive token
bounds (the open-ended last bin has no `max_input_tokens`), counts, costs, unpriced calls,
per-call cost and request/cost percentages. Sizes include cached input; output-only
calls are excluded. Empty bins are known zero counts. With no matching calls, or
when all calls are unpriced, the corresponding percentages are omitted. Cost
percentages describe **known estimated spend** and can be incomplete.

No category costs, answer-quality scores, licensed-user adoption rates, time
savings, or ROI are inferred. Operations and anomalies remain outside these tools.

## Tests

`go test ./internal/mcp` exercises SDK discovery and tool calls over HTTP,
the admin gate, per-request identity, filters, stored accounting semantics,
grouped totals, category counts/shares/trends, percentage denominators, rounding,
portable output schemas, invalid inputs, limits, empty data, cancellation, the
development auth bypass, the CORS allowlist, and generic error responses. Further HTTP tests cover
observed-user identity deduplication, new/returning history, application/anonymous
exclusions, prompt-size bin boundaries, cost coverage and empty distributions.
Run `go test ./...` for backend regression coverage after building the embedded
web assets (`npm ci && npm run build` in `web`).
