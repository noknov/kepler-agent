# Version policy

`main` is the v2 line. It contains the shared harness used by both the local
CLI and hosted Slack agent; new fixes and features target this architecture.

The final v1 source snapshot is the `v1-final` tag at commit `49380a51`. v1
maintenance ended on 2026-08-11. It receives no security fixes, compatibility
work, schema migrations, or releases. Operators that still run v1 should pin
that tag while migrating; do not mix v1 runtime code with the v2 schema or
deployment configuration.

- [v2 documentation](v2/README.md)
- [archived v1 notes](v1/README.md)
