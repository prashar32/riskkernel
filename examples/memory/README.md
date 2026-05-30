# Git-native memory

RiskKernel's memory layer is **a directory of files you own** — version them in
git, edit them in your editor. RiskKernel only reads them (it never rewrites your
markdown). Retrieval is deterministic: list, read, and keyword search. There is
**no embedding index / vector DB** in v0.1 (it's a future opt-in).

Point the daemon at a directory:

```bash
RISKKERNEL_MEMORY_DIR=./memory riskkernel serve
```

Organize by **namespace** (a subdirectory). Files are `.md` / `.markdown`,
`.yaml` / `.yml`, or `.txt`. Markdown files may carry simple `key: value`
frontmatter — `title` and `description` are surfaced in listings:

```
memory/
  notes/
    architecture.md
    decisions.md
```

```markdown
---
title: Architecture decisions
description: why we chose SQLite + a pure-Go driver
---

# Architecture

We use a single WAL-mode SQLite file as the default store ...
```

Read it from the daemon (agents) or the CLI (you):

```bash
# files
curl "http://localhost:7070/v1/memory?namespace=notes"
curl "http://localhost:7070/v1/memory/entry?namespace=notes&name=architecture.md"
riskkernel memory list notes
riskkernel memory show architecture.md notes

# episodic facts (small key/value an agent accumulates during runs)
curl -X PUT http://localhost:7070/v1/memory/facts \
  -d '{"namespace":"notes","key":"db","value":"sqlite"}'
curl "http://localhost:7070/v1/memory/facts?namespace=notes"
```

From the Python SDK:

```python
import riskkernel as rk
c = rk.RiskKernel()
c.list_memory(namespace="notes")
c.read_memory("architecture.md", namespace="notes")
c.put_fact("notes", "db", "sqlite")
```
