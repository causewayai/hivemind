# Hivemind — Product Requirements Document

## Overview

Hivemind is a multiuser memory management system for AI harnesses, delivered in two editions: a local single-user edition and a cloud subscription-based team edition. Both editions expose the same MCP interface and CLI, allowing AI harnesses and ETL pipelines to read from and write to a persistent, searchable memory store without changing their integration code when switching editions.

---

## Editions

### Local Edition

- Runs as a local daemon on a single machine
- Requires no authentication — identity is the OS user
- All locally running harnesses share memory implicitly; no scope gates apply
- Backed by SQLite (structured data) and sqlite-vec (vector embeddings)
- Intentionally limited feature set relative to the cloud edition
- Distributed as a single self-contained binary

### Cloud Edition

- Multi-user, subscription-based service
- Authenticates users via OAuth/SSO (no password-based auth)
- Backed by PostgreSQL (users, teams, permissions, metadata) and Elasticsearch (memory content, embeddings, hybrid search)
- Supports hierarchical team structures with role-based access
- Full feature set including team sharing, admin controls, audit logs, and governance tools

---

## Memory Model

### Memory Entry Structure

Every memory entry — whether written by a harness or an ETL pipeline — shares the same envelope:

| Field | Description |
|---|---|
| `id` | Unique identifier |
| `content` | Freeform text body of the memory |
| `scope` | Visibility scope (session, user, team, sub-team) |
| `source` | Identifier of the harness or pipeline that wrote it |
| `source_type` | `harness` or `etl` |
| `external_id` | Optional external reference (e.g., Jira ticket ID) for ETL deduplication. Unique per `(source, scope)`, not globally — the same external ID can exist independently in different scopes (e.g., two teams each ingesting their own copy of Jira project PROJ) |
| `tags` | Optional user-defined labels |
| `created_at` | Timestamp of creation |
| `updated_at` | Timestamp of last update |

There is no enforced type taxonomy. Content is freeform; structure comes from tags and scope, not from a rigid schema.

### Memory Scopes

| Scope | Local Edition | Cloud Edition |
|---|---|---|
| Session | Harness-owned, not visible to other harnesses | Harness-owned, not visible to other harnesses |
| User | All local harnesses share implicitly | Shared across all harnesses operating as the same user (configurable) |
| Team | N/A | Visible to all members of a team and its sub-teams |
| Sub-team | N/A | Visible to sub-team members and parent team admins |

### Scope Promotion

Harnesses **cannot** promote a memory to a broader scope on their own. Promotion from session → user or user → team requires explicit human action via the CLI or web interface. This is a hard requirement and cannot be bypassed programmatically.

---

## Retrieval

Memory retrieval uses a hybrid model combining semantic search and structured filtering:

- **Semantic search** — queries are converted to embeddings and matched against stored memory vectors
- **Tag/namespace filtering** — results can be filtered by tags, scope, source, or source type before or after semantic ranking
- Retrieval is **on-demand** — harnesses query when they need information; there is no automatic context injection at session start

Embedding generation is provider-agnostic. The system exposes a pluggable embedding interface; the specific model or provider is configured by the user or administrator. A connecting harness's own LLM/embedding endpoint is a valid plug-in target — the interface does not require a separately hosted embedding service, so a harness can supply embeddings via whatever model it already has credentials for.

---

## Write Paths

Two write paths are supported, both using the same API and schema:

**Harness writes** — real-time, single-entry writes as a harness discovers information during a session. Scoped to the harness session by default.

**ETL pipeline writes** — bulk writes from external sources (e.g., Jira, GitHub, source code indexers). Support upsert semantics via `external_id` to prevent duplication on re-ingestion. Uniqueness is enforced on the composite `(source, external_id, scope)` — not on `external_id` alone — so the same external record can be independently ingested into multiple scopes (e.g., separate teams) without colliding. This composite key is indexed to keep upsert lookups fast at bulk-ingestion volumes. ETL-written memories can target any scope permitted by the pipeline's credentials.

---

## Interfaces

### MCP Interface

- Primary interface for AI harnesses
- Exposes tools for: querying memory (read), writing memory (write), and listing available scopes
- Harnesses have full CRUD on their own session scope
- Harnesses have read-only access to user, team, and sub-team scopes
- Promoting a memory to a broader scope is not an MCP operation — it is a human action
- MCP tool definitions are identical between local and cloud editions; the endpoint is configurable

### CLI

- Primary interface for humans (end users and administrators)
- Unified command structure across editions; cloud edition adds admin subcommand group
- **User commands:** query memory, view memories, tag memories, promote memory scope, delete memories
- **Admin commands (cloud only):** manage teams and sub-teams, manage user roles, view audit logs, configure sharing policies, manage harness credentials

---

## Team Structure (Cloud Edition)

- Teams are hierarchical — teams can contain sub-teams to arbitrary depth
- Memory scope follows bidirectional inheritance:
  - Sharing to a team makes memory visible to all sub-teams beneath it (downward)
  - Parent team admins can see all memory in their sub-teams (upward)
- Each team member has a role: **Owner**, **Admin**, **Contributor**, or **Viewer**
- Roles control read/write/share permissions within and across team boundaries

---

## Governance and Data Safety

### Harness Identity

- Each harness instance authenticates with its own credential (API key or token)
- Every memory write is stamped with the harness identity
- Harness credentials can be scoped to limit what they can read or write

### Audit Logging (Cloud Edition)

- All read and write operations are logged with harness identity, timestamp, and affected memory entries
- Audit logs are accessible to team admins and owners
- Logs cannot be modified or deleted by harnesses

### Human-in-the-Loop Sharing

- Harnesses cannot share memory beyond their current scope
- Scope promotion requires an authenticated human action
- This applies to: session → user, user → team, sub-team → parent team

### Intra-User Sharing (Cloud Edition)

- By default, harnesses operating as the same user share memory at the user scope
- Users can opt in to session isolation, preventing cross-harness sharing even within their own account
- This setting is per-user and configurable via CLI or web interface

### Content Policy Hooks (Cloud Edition)

- Pluggable filter hooks that run before a memory is written to a shared scope
- Hooks can detect and reject sensitive content (PII, credentials, confidential patterns)
- Hook configuration is managed by team admins

---

## Non-Goals

- Hivemind does not train or fine-tune AI models
- Hivemind does not orchestrate or coordinate harness behavior
- Hivemind does not replace context windows — it supplements them with persistent external memory
- Hivemind does not provide a general-purpose document or object store
- Hivemind does not allow harnesses to autonomously share memory across scope boundaries

---

## Technology Constraints

| Concern | Local Edition | Cloud Edition |
|---|---|---|
| Language | Go | Go |
| Structured store | SQLite | PostgreSQL |
| Vector/search store | sqlite-vec | Elasticsearch |
| Authentication | None (OS user) | OAuth / SSO |
| Distribution | Single binary | Service deployment |
| Embedding provider | Pluggable (configurable, including the connecting harness's own LLM) | Pluggable (configurable, including the connecting harness's own LLM) |

---

## Success Criteria

- A locally running harness can write a memory entry and retrieve it in a subsequent on-demand query within the same session
- Multiple harnesses running locally can read each other's user-scoped memories without configuration
- A cloud harness cannot read memories outside its permitted scope
- Promoting a memory from session to user scope requires a human CLI action — it cannot be done via MCP
- An ETL pipeline can upsert memory entries from an external source without creating duplicates
- A team admin can view all memory written within their team hierarchy
- Switching a harness from local to cloud endpoint requires only a configuration change
