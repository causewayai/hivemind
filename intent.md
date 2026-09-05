# Hivemind — Intent

## What Is This?

Hivemind is a memory management system for AI harnesses. It gives AI agents a place to store, retrieve, and share discovered knowledge — facts, preferences, conversation context, entities, project structure, and any other information an AI harness encounters during its work.

## Why Does This Exist?

AI harnesses operate ephemerally. Each session starts fresh, and knowledge discovered in one session is lost when it ends. When multiple harnesses run concurrently — or when a team runs harnesses across many users — there is no shared foundation of knowledge. Hivemind solves this by providing a persistent, queryable, and scoped memory store that harnesses can read from and write to on demand.

## Who Is It For?

- **Individual developers** using AI harnesses locally, who want memory to persist across sessions and be shared between concurrently running harnesses on the same machine
- **Teams** using cloud-based or distributed harnesses, who want to share institutional knowledge across members while keeping personal or sensitive memory private
- **ETL pipelines and integrations** that load external knowledge (e.g., from Jira, GitHub, source code) into the memory store for harnesses to consume

## Core Principle

Harnesses are consumers and contributors of memory, but they are not governors of it. Harnesses can discover, write, and read memory — but they cannot promote memory across scope boundaries without human involvement. Humans decide what gets shared and with whom.

## What It Is Not

- Not a general-purpose database or document store
- Not a training data pipeline or fine-tuning system
- Not a harness orchestration or agent coordination system
- Not a replacement for context windows — it supplements them
