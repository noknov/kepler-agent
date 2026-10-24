# Maintenance backlog

Reviewed 2026-10-24. These are bounded follow-up opportunities, not claims of
failed product behavior. Preserve an item only while its acceptance remains
useful. Link a tracked issue/PR when implementation starts instead of maintaining
a second status ledger. Product defects belong in GitHub issues.

| Area and evidence | Next useful trigger | Acceptance |
| --- | --- | --- |
| Slack `Service` combines run admission, control routing, OAuth continuation, and reply setup in [service.go](../packages/surfaces/slack/agent/service.go) | A change to one of these lifetimes | Delegate at a real ownership boundary; existing actor/control/Stop/delivery race suites pass, no second routing/state path is introduced |
| Connection [file store](../packages/connections/filestore.go) repeats transaction acquisition across methods | Next file-storage operation | Centralize the complete read/modify/replace transaction if it reduces duplicated correctness logic; preserve cross-instance and CAS regressions |
| GitHub [tool adapter](../packages/tools/github/tools.go) has many resource operations in one owner | Add/remove an API resource | Keep exact effect/allowlist contracts; group by cohesive resource responsibility when that reduces coupling, rather than splitting to hit a line quota |
| Imported CLI UI has a broader type scope than the owned protocol integration | Update imported UI or SDK bindings | Inspect `pnpm typecheck:vendor` separately; resolve ownership of diagnostics instead of weakening `pnpm typecheck` or pretending the narrow gate covers all imported code |
| Go repository checks inspect syntax and static SQL literals, not typed/dynamic SQL dataflow | Introduce dynamic SQL construction or new driver wrappers | Keep DDL in deployment migrations; extend a check with a demonstrated negative fixture if the new execution shape would evade it |

Current automated evidence is available through `make change-report BASE=<ref>`
and CI artifacts. File sizes and growth highlight inspection scope; they do not
measure architectural quality. There is no scheduled cleanup agent or automatic
baseline refresh hidden behind this document.
