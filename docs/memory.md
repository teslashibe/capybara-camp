# Memory deferred

The owner removed memory and Cortex consolidation from V1 on October 22, 2026,
pending Cortex readiness. Shared Markdown skills remain in V1.

V1 does not expose memory HTTP routes, a memory inference CLI command, a memory
screen or consolidation jobs. Server startup, readiness, migrations and release
containers do not require Cortex. Generic tool-disabled CLI inference transport
remains internal and tested; it is not an exposed memory feature.

The experimental implementation and its specification are preserved in commit
`c6dedd252fab584a1a5d526d9a5f873e40c2ca29`. Follow-up work is tracked in
[deferred issue #5](https://github.com/teslashibe/capybara-camp/issues/5), outside
the V1 release dependencies. Reintroduction requires an explicit scope decision
and fresh integration and permission tests after Cortex is ready.

Historical Camp migrations 30 and 31 remain unchanged. They preserve schema
compatibility and existing development records; retained-data downgrade guards
still apply. The server excludes old memory events from replay and does not run
pending memory jobs. Any existing Cortex schema remains untouched. Back up the
complete database before upgrades; removing this feature does not authorize
dropping stored data.
