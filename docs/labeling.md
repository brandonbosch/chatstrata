# Labeling

Classifier and Jev labeling are not included in chatstrata 2. The archive's
compatible label tables remain available for SQL compatibility, but v2 does
not populate them or migrate legacy labels. Agents can analyze archived
conversations through MCP and read-only DuckDB queries.

Historical labeling code and documentation are preserved at the `python-final`
Git tag. Adding labeling to Go is future work, not an installation prerequisite.
