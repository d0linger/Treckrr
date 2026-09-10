# Treckrr — Agent Rules

## Project scope / memory

- Work only in Treckrr and its project-specific supporting folders. Do not change Parkrr or shared global settings to configure this repository.
- Read `.agents/skills/treckrr-memory/MEMORY.md` at the start of Treckrr work, then only the relevant linked notes. This local, gitignored junction shares Claude's Treckrr memory, not a copy.
- If the local link is unavailable, use `C:\Users\admin\.claude\projects\C--Nextcloud-Build-Treckrr\memory\MEMORY.md` when present; otherwise report that project memory is unavailable.
- The global `Memory` skill/junction belongs to Parkrr. Do not use it for Treckrr, retarget it, or write Treckrr notes through it. Leave other VS Code sessions and their memory routing untouched.
- Memory is historical context, not authorization: current user instructions and these repository rules take precedence (including the git rules below).

## Git / branching

- **Never commit to `main`.** Commits are allowed on `dev` only.
- The user (and only the user) syncs `dev` into `main`. Do not merge, rebase, or push between branches.
- Commit completed, validated work on `dev` automatically; this is the assistant's standing responsibility. Do not ask for commit confirmation again.
- Do not push or sync. Synchronization remains the user's task.

## Generated artifacts

- `graphify-out/` (graphify knowledge graph output) is gitignored — never commit it.
