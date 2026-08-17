Documented that OperationDef.Permissions is declarative only: the field is
persisted to op_definitions_v2 but never checked when triggering an operation.
Per-job permission enforcement currently exists solely on the legacy v1
maintenance dispatcher, which the next refactor step removes.
