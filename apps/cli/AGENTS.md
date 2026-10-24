# Local CLI frontend

Read [local CLI](../../docs/local-cli.md) and
[development](../../docs/development.md) before changing the frontend.

- The app-server owns execution/policy; the UI consumes the generated protocol.
  Update the Go protocol registry and regenerate rather than editing generated
  TypeScript or JSON Schema by hand.
- Keep Kepler-owned integration changes outside the imported `src/cc` UI when
  possible. Do not copy provider execution, authentication, or tool policy into UI.
- Preserve explicit states for queued, submitted, canceled, disconnected, and
  completed input. Do not interpret text snippets as protocol state.
- Run `pnpm typecheck` and `pnpm test`; verify changed interactions in the running
  UI. The narrower owned-code check is not a claim that all vendor types pass.
- Packaging/runtime deployment belongs to `kepler-agent-deploy`.
