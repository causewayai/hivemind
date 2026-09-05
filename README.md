# Hivemind (Local Edition)

Hivemind gives AI harnesses a persistent, queryable memory store — a place to
write down facts, preferences, and context discovered during a session, and
read them back later or from another harness running on the same machine.

This repository currently contains the **Local Edition daemon**: a single
binary (`hivemindd`) that runs on your machine and speaks
[MCP](https://modelcontextprotocol.io) (Model Context Protocol) over HTTP.
There is no CLI or web UI yet — see [Limitations](#limitations) below.

For the full product vision, see [`intent.md`](intent.md) and
[`docs/PRD.md`](docs/PRD.md). For implementation notes and open design
questions, see [`docs/DESIGN.md`](docs/DESIGN.md).

## Requirements

- Go 1.25+
- A C compiler (the SQLite and vector-search dependencies use cgo — on
  macOS, the Xcode Command Line Tools; on Linux, `gcc` or `clang`)

## Install / Build

```bash
git clone https://github.com/causewayai/hivemind.git
cd hivemind
make build
```

This produces a `hivemindd` binary in the repository root. There's no `go
install` path yet since the binary isn't published to a module proxy-visible
location — build from a local clone.

## Running the daemon

```bash
make run
# or, directly:
./hivemindd
```

By default, `hivemindd` listens on `127.0.0.1:8420` **only** — it never
binds to a non-loopback address, so it's not reachable from other machines.
Data is stored in a SQLite database at `~/.hivemind/hivemind.db`.

### Configuration

All configuration is via environment variables; there is no config file.

| Variable | Default | Description |
|---|---|---|
| `HIVEMIND_PORT` | `8420` | TCP port to listen on (loopback only) |
| `HIVEMIND_DATA_DIR` | `~/.hivemind/hivemind.db` | Path to the SQLite database file |
| `HIVEMIND_EMBEDDING_DIM` | `768` | Dimensionality of stored embeddings — see [Embeddings](#embeddings-current-limitation) |

Example, running on a custom port with an isolated data file (useful for
trying things out without touching your real data):

```bash
HIVEMIND_PORT=9000 HIVEMIND_DATA_DIR=/tmp/hivemind-test.db ./hivemindd
```

Stop the daemon with `Ctrl-C`, or `kill` its process — there's no separate
stop command.

## Connecting a harness

Point your MCP client at `http://127.0.0.1:8420/` using the Streamable HTTP
transport. The daemon is **stateless** (no session cookies/headers to
manage) — every tool call is a self-contained JSON-RPC request.

You are responsible for generating and passing a `session_id` on every
`memory_write`/`memory_query` call. This is how the daemon knows which
harness session a memory belongs to and enforces the isolation described
below — it is not derived from the MCP transport session. Use any string
that's stable for the lifetime of your harness session (e.g. a UUID
generated once at startup).

### Manual smoke test

If you want to confirm the daemon is reachable without writing a client:

```bash
curl -s -X POST http://127.0.0.1:8420/ \
  -H 'Content-Type: application/json' \
  -H 'Accept: application/json, text/event-stream' \
  -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2026-07-28","capabilities":{},"clientInfo":{"name":"smoke-test","version":"0.0.1"}}}'
```

A healthy daemon responds with a JSON-RPC result containing `serverInfo` and
`capabilities`.

## Available tools

### `memory_write`

Writes a new memory entry. Always scoped to the calling session — a harness
cannot write directly to a broader scope (see [Scopes](#scopes-and-isolation)).

| Field | Required | Description |
|---|---|---|
| `session_id` | yes | Your harness session's identifier |
| `content` | yes | The freeform text to remember |
| `source` | yes | An identifier for the harness/tool writing this (e.g. `"claude-code"`) |
| `tags` | no | Labels for later filtering |
| `embedding` | no | A precomputed embedding vector. If omitted, the daemon generates one itself (see [Embeddings](#embeddings-current-limitation)) |

Returns `{"id": "<uuid>"}`.

Example call (as a `tools/call` JSON-RPC request body):

```json
{
  "jsonrpc": "2.0", "id": 2, "method": "tools/call",
  "params": {
    "name": "memory_write",
    "arguments": {
      "session_id": "sess-demo",
      "content": "the build is broken on main",
      "source": "claude-code",
      "tags": ["ci", "urgent"]
    }
  }
}
```

### `memory_query`

Searches memory using free-text (semantic) search combined with optional
tag/source filters. Returns your own session's matching entries **and**
matching `user`-scope entries shared across all local harnesses — never
another session's entries (see [Scopes](#scopes-and-isolation)).

| Field | Required | Description |
|---|---|---|
| `session_id` | yes | Your harness session's identifier |
| `query` | no | Free text to search for |
| `tags` | no | Only return entries with at least one of these tags |
| `source` | no | Only return entries written by this source |
| `top_k` | no | Max results to return (default 10) |

Example call and response, following on from the `memory_write` example
above:

```json
{
  "jsonrpc": "2.0", "id": 3, "method": "tools/call",
  "params": {
    "name": "memory_query",
    "arguments": {
      "session_id": "sess-demo",
      "query": "the build is broken on main",
      "top_k": 5
    }
  }
}
```

```json
{
  "results": [{
    "ID": "271b53c5-5b6c-4fdb-af8d-57bafc14fac1",
    "Content": "the build is broken on main",
    "Scope": "session",
    "SessionID": "sess-demo",
    "Source": "claude-code",
    "SourceType": "harness",
    "ExternalID": "",
    "Tags": ["ci", "urgent"],
    "CreatedAt": "2026-09-05T11:58:53Z",
    "UpdatedAt": "2026-09-05T11:58:53Z"
  }]
}
```

(Result field names are capitalized as shown — this is a known rough edge,
not a typo; see [Limitations](#limitations).)

### `list_scopes`

Takes no input. Returns the scopes available in this edition:

```json
{"scopes": ["session", "user"]}
```

## Scopes and isolation

- **`session`** — private to the harness session that wrote it. Other
  sessions, including other harnesses running locally, cannot see it.
- **`user`** — shared across every local harness session. There's no
  concept of separate users in the Local Edition; anything at `user` scope
  is visible to any harness that queries it.

**A harness cannot promote its own memory from `session` to `user` scope.**
This is intentional (see `intent.md`'s "Core Principle") — that decision
requires explicit human action, and the Local Edition doesn't yet have a
CLI or UI to perform it. In practice, everything a harness writes today
stays at `session` scope for the lifetime of that session.

## Embeddings (current limitation)

Out of the box, `hivemindd` uses a deterministic **hash-based** stand-in for
a real embedding model (`internal/embedding.HashProvider`). It is
explicitly **not semantic** — it exists so the daemon is fully runnable and
testable without any external model configured. This means:

- `memory_query`'s free-text search will reliably find entries whose
  content is identical or near-identical to your query text, but will
  **not** find entries that are only conceptually related (paraphrases,
  synonyms, etc.).
- If you write content and then query with unrelated wording, expect
  `results: []`/`null` — this is expected with the default provider, not a
  bug.

A harness can work around this today by supplying its own precomputed
`embedding` on `memory_write` (using whatever model it already has
credentials for — see `docs/PRD.md`'s Retrieval section). A real pluggable
embedding provider (e.g. an HTTP call to an OpenAI-compatible endpoint) is
planned but not yet implemented.

## Data and backups

All data lives in one SQLite file at `HIVEMIND_DATA_DIR` (plus its
`-wal`/`-shm` sidecar files while the daemon is running). To back up or
reset your memory store:

```bash
# Back up (stop the daemon first, or accept a WAL-consistent copy)
cp ~/.hivemind/hivemind.db ~/hivemind-backup.db

# Reset (deletes all memory)
rm ~/.hivemind/hivemind.db ~/.hivemind/hivemind.db-wal ~/.hivemind/hivemind.db-shm
```

## Development

```bash
make test   # run the test suite
make lint   # go vet + golangci-lint (naming, architecture, correctness checks)
make check  # lint + test
```

See [`docs/DESIGN.md`](docs/DESIGN.md) for architecture notes, and
[`docs/plans/2026-09-04-local-daemon.md`](docs/plans/2026-09-04-local-daemon.md)
for the implementation plan this daemon was built from.

## Limitations

This is an early Local Edition MVP. Known gaps, in rough order of impact:

- **No CLI or UI** — no way to promote memory to `user` scope, browse/edit/
  delete entries, or manage tags, except by talking MCP directly.
- **No real embedding provider** — see [Embeddings](#embeddings-current-limitation) above.
- **No ETL/bulk-write path** — no `external_id`-based upsert for ingesting
  from Jira, GitHub, etc. yet.
- **Result JSON uses Go's default field casing** (`ID`, `SessionID`, ...)
  rather than `snake_case` — a client consuming `memory_query` results
  needs to account for this until it's normalized.
- **Not a fully static binary** — despite being one file, `hivemindd`
  still dynamically links the host's libc (a consequence of the cgo-based
  SQLite/vector-search dependencies). A Linux build isn't guaranteed to run
  on an arbitrary/older glibc. See `docs/DESIGN.md`.

None of this affects the single-machine, single-user use case the Local
Edition targets today, but plan accordingly if you're scripting against it.
