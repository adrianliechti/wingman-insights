# Security Review

Scope: uncommitted working-tree changes (MCP handler, tools, `main.go`).

## Summary

| # | Severity | File | Lines | Vulnerability | Confidence |
|---|----------|------|-------|---------------|------------|
| 1 | 🟠 HIGH | internal/mcp/handler.go | 90-107, 155-189 | MCP admin check relies on JWT claims that are never verified (anyone can forge admin) | 8/10 |

## Findings

### 1. MCP accepts forged JWTs as admin (HIGH, 8/10)

**Location:** `internal/mcp/handler.go`: the admin check is at lines 101-106; the token is read by `tokenPrincipal` at lines 155-189.

**Issue:** MCP access is admin-only, and the `!p.admin` check is the only thing enforcing that. `tokenPrincipal` base64-decodes the bearer JWT payload and trusts its `oid`/`sub` and `groups` claims. It never checks the signature, issuer, audience or expiry. When `INSIGHTS_MCP_ENABLED=true`, `/mcp` is mounted directly on the public mux (`main.go:129-133`).

**Exploit scenario:** An attacker writes a token with payload `{"oid":"a","groups":["<INSIGHTS_ADMIN_GROUP>"]}` and calls tools such as `get_usage` (`group_by=user|department`, `view=adoption`) or `get_categories`. This returns org-wide per-user cost, department, location and topic data. The admin group ID is an identifier, not a secret.

**Inconsistency:** The dashboard does verify tokens (`internal/api/auth.go:149`, `h.verifier.Verify(...)`). MCP is therefore weaker than the dashboard, although the code comment says it applies "the same rule".

**Recommended fix:** Before trusting any claims, verify the bearer token with the same OIDC verifier the dashboard uses (`COMPANION_API_ISSUER` / `COMPANION_API_AUDIENCE`). Alternatively, refuse to serve `/mcp` unless a trusted proxy-authentication mode is configured. Until then, treat `INSIGHTS_MCP_ENABLED=true` as unsafe on any network-reachable deployment.
