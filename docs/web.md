# Hosted Web

The Web entry runs on the hosted worker and uses the same model, prompt policy,
workspace, PostgreSQL, Redis, run events, and server tool policy as Slack.
Browser conversations are separate from Slack messages. Web users do not
inherit Slack connection or preference records.

## Request path

```text
browser -> public gateway -> worker Web handler -> hosted profile
```

The gateway keeps `/slack/*` and `/oauth/*` on their existing handlers and
proxies other paths to `WEB_UPSTREAM_URL`. Set `WEB_PUBLIC_BASE_URL` only when
the Web origin differs from `CONNECTIONS_PUBLIC_BASE_URL`.

## Configuration

| Variable | Purpose |
|---|---|
| `WEB_ENABLED` | Enables the gateway proxy and worker Web handler |
| `WEB_UPSTREAM_URL` | Gateway-only internal worker URL |
| `WEB_PUBLIC_BASE_URL` | Public origin; defaults to `CONNECTIONS_PUBLIC_BASE_URL` |
| `WEB_SESSION_SECRET` | At least 32 random characters; store only in secrets |
| `WEB_SESSION_TTL` | Browser session lifetime, up to 31 days |
| `WEB_SITE_NAME` | Display name; defaults to `Kepler` |
| `WEB_STATIC_DIR` | Optional directory for Web UI files; when set, assets are read from disk instead of the embedded bundle |

When `WEB_STATIC_DIR` is set, edit files under `packages/surfaces/web/static/` and refresh the browser. No worker rebuild is required for UI-only changes.

The browser UI is plain ES modules under `packages/surfaces/web/static/`. Assistant messages are rendered with vendored [marked](https://marked.js.org/) and [DOMPurify](https://github.com/cure53/DOMPurify) (`static/vendor/`). The hosted worker adds a Web-only product prompt that requires valid GitHub-Flavored Markdown (triple-backtick code fences, no two-backtick fences).

The Slack OpenID Connect redirect URI is:

```text
https://<public-origin>/auth/slack/callback
```

Add that exact HTTPS URI to the Slack app. The Web entry reuses
`SLACK_OAUTH_CLIENT_ID` and `SLACK_OAUTH_CLIENT_SECRET`.

## Authentication and ownership

- Slack OpenID Connect uses discovery, PKCE, nonce, a one-time database state,
  and a browser-bound login transaction cookie.
- ID tokens require a valid RS256 signature, issuer, audience, expiry, nonce,
  Slack workspace ID, and Slack user ID.
- `ALLOWED_SLACK_USERS` is checked at sign-in and on every authenticated
  request.
- Browser cookies are opaque, Secure, HttpOnly, SameSite=Lax, and stored only
  as SHA-256 hashes in PostgreSQL.
- Mutating APIs require an exact same-origin `Origin` and an HMAC CSRF token.
- Every conversation query includes provider, tenant, and subject ownership.
- Streaming events expose presentation-safe fields and redact secrets. Raw
  tool arguments and results are not sent to the browser.

The identity provider is an interface and registry. Slack is the only provider
implemented now; additional OIDC or registration providers can be added
without changing conversation ownership or browser session storage.

## Schema

Apply the deployment migration before enabling the worker. Fresh installs use
the matching tables in `schema/postgres.sql`:

- `web_auth_states`
- `web_auth_sessions`
- `web_conversations`
