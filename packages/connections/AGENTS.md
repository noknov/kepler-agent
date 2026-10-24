# Connection ownership

Read [safety](../../docs/safety.md) and the connection upgrade contract in
[development](../../docs/development.md).

- Browser identity, operator provider identity, and user integration credentials
  are distinct. Resolve the actor and connection instance explicitly.
- Read credential/token metadata from one storage snapshot. Refresh/enrichment
  writes are conditional on the durable revision and cannot insert a deleted row.
- A process-local refresh mutex reduces duplicate work; storage CAS provides
  cross-worker correctness. Conflicts cannot return an old snapshot's token.
- File transactions require the OS lock over the complete read/modify/replace
  operation. PostgreSQL changes use operator-applied migrations, never runtime DDL.
- Reproduce concurrent delete, reauthorization, refresh, and metadata enrichment
  with controlled transports. Include the PostgreSQL integration suite when
  changing the storage contract; file-store tests alone do not prove SQL behavior.
