---
name: recoil
description: >-
  This skill should be used to keep operational memory for this repo via the
  `recoil` CLI — recall past mistakes before acting, encode the lesson when
  something goes wrong, and act on whatever surfaces. Run `recoil recall` and
  `recoil guard` BEFORE the first Edit/Write/MultiEdit to a file, or before
  running a test suite, a build, or a migration, to surface what went wrong
  here before — then read and apply what fires. Run `recoil encode` AFTER a
  surprise: the user corrects or reverts a change (says "no", "that's wrong",
  "don't do that", "undo"), a test or build expected to pass fails, or a
  command exits non-zero unexpectedly (a `git revert`, a broken build, a
  failing assertion). Triggers: correction, revert, test-fail, error. Not for
  read-only questions, pure exploration, or anything that changes no files.
---

# recoil — operational memory for this repo

`recoil` is a local CLI backed by a plain-text store (`.recoil/store.tsv`). It
remembers what went wrong here before — a failed command, a revert, a
correction — and surfaces it when the same thing is about to happen again.
Matching is deterministic keyword overlap: no embeddings, no network. The loop
is three moves: **recall + guard before touching files, encode after a surprise,
act on whatever fires.** Running `recall`/`guard` is safe to attempt even when
unsure recoil is installed — if it isn't on `PATH` the command simply isn't
found, and nothing is harmed.

## Decide in one read

- **About to emit an Edit / Write / MultiEdit tool call, or run a test suite,
  build, or migration?** Run recall + guard FIRST — every time, even for a
  one-line change, even on a file that feels familiar. The moment you're about
  to change a file is the trigger instant; do not let "this is trivial" or "I
  know this code" talk you out of it. Once per file/situation per session is
  enough — no need to re-run before every edit to the same file.
- **Just got surprised** — a correction, an unexpected test/build failure, a
  revert? `encode` it BEFORE moving on, while the cue tokens are still in front
  of you.
- **Pure read-only question or exploration that changes nothing?** Skip it.

Do not deliberate over whether a moment "counts." If a file change or a
test/build is next, the answer is recall + guard.

## Before changing code or running tests — recall + guard, then ACT

Pass the files in play, or pipe what you're doing straight in (situation,
`--files`, and stdin are all combined). If unsure what to pass, pipe the diff:

```sh
recoil recall --files src/runner.cs,tests/EditModeTests.cs
recoil guard  --files src/runner.cs,tests/EditModeTests.cs
git diff --staged | recoil recall          # or: echo "editing the EditMode runner" | recoil recall
```

A `recall` hit looks like this — recognize it and read it (`hits` rises each
time it's recalled, so a high count means it keeps mattering):

```
>> Assert.ThrowsAsync hangs the EditMode runner; use a sync assert instead
   [test-fail w=2 hits=3] matched: editmode assert throwsasync runner
```

The same lesson as a `guard` warning (printed to stderr) — two views of one
memory:

```
recoil: been burned here before — Assert.ThrowsAsync hangs the EditMode runner
```

**Act on what fires — this is the point, not a box to tick.** For each hit, say
in one line what it changes: avoid the known-bad path, or name the specific
token that differs and why the lesson doesn't apply here. "Probably fine" is not
an answer. A `guard` warning is a real hazard — it fires only on memories of
things that actually went wrong (error / revert / correction / test-fail, never
plain notes) and only when ≥2 cue tokens overlap, so it is not noise; do not
proceed past it without addressing it. No output means nothing prior matched —
clear to proceed. (`guard` is read-only; `recall` reinforces what it matches and
keeps useful lessons strong, so prefer `recall` when you want the good ones to
stay near the top.)

## After a surprise — encode the lesson

**Surprise test:** encode only when your model of the repo was *wrong* — you
predicted X and got Y. A correction, an unexpected failure, or a revert
qualifies. A self-inflicted typo or a compile error you immediately understand
is routine — skip it. Encode when the failure revealed something non-obvious
about *this* repo (a hidden dependency, config in an unexpected place, a tool
that behaves unusually here). General best practices and routine notes do NOT
belong here — they go in normal docs.

A failure is also a *recall* moment: before re-fixing, pipe the error text into
`recoil recall` to check whether this exact failure is already remembered.

Match the trigger to the event — higher weight surfaces sooner:

| What just happened (the surprise) | `--trigger` | weight |
|---|---|---|
| User corrected a non-obvious choice ("no" / "that's wrong" / "don't do that") | `correction` | 3 |
| A change was reverted or `git revert`ed | `revert` | 2.5 |
| A test or build expected to pass failed | `test-fail` | 2 |
| A command exited non-zero unexpectedly | `error` | 1.5 |
| Something worth keeping, recorded by hand | `manual` | 1 |

Keep `--gist` to one actionable line. Make `--cue` the tokens the *next*
occurrence will share — this event's real file names, error text, and system
names. `guard` needs ≥2 overlapping cue tokens to fire, and the matcher drops
stopwords and single-character tokens, so a one-word or generic cue silently
never recalls again.

- Bad: `--cue "bug"` — one generic token; never re-fires.
- Good: `--cue "editmode assert throwsasync runner hang"` — the tokens the next
  hang will share.

Worked examples — fill the cue with *this* surprise's own tokens:

```sh
recoil encode --trigger correction \
  --gist "Config lives in app.toml, not config.yaml — yaml is the old path" \
  --cue  "config app toml yaml settings load"

recoil encode --trigger test-fail \
  --gist "Don't name a Unity folder Build/, .gitignore untracks it" \
  --cue  "unity build folder gitignore"

recoil encode --trigger revert \
  --gist "Bumping the proto version breaks the v1 clients still in prod" \
  --cue  "proto version bump v1 client breaking"
```

Or let a command record its own failure by wrapping it — but note `watch`
always tags the record `error` (weight 1.5), even for a test command. When the
weight matters, `encode` manually with `--trigger test-fail`:

```sh
recoil watch -- go test ./...
```

## Cross-tool note

Behavior is identical on Claude Code and Codex — it depends on invoking the CLI,
not on any host's hook. On **Claude Code** a warn-only guard hook fires
automatically on Edit/Write/MultiEdit, so a `been burned here before` line may
appear without you invoking anything — that is expected, it never blocks, and
running `recall` yourself is still this skill's job. On **Codex** there is no
auto-hook, so recall + guard are entirely your responsibility.

An auto-recorded revert (from the git post-commit hook) has no human-written
gist, so still `encode` the actual lesson — what the reverted change got wrong
is the valuable part the bare hook can't capture.

## Setup and housekeeping

Once per repo: `recoil init`. If the binary is missing from `PATH`, see the
README install steps. Housekeeping (not part of the per-edit loop):

- `recoil list` — show everything stored.
- `recoil decay` — forget faded lessons (strength halves every ~30 unused days;
  recall renews it, so the ones that keep mattering stay).
- `recoil hook --install` — wire the git pre/post-commit guard and revert
  recorder; won't overwrite existing hooks. The post-commit recorder only
  captures commits whose subject starts with `Revert`.
