# recoil

A local-first memory for AI coding agents. One static binary, one plain-text
store, no embeddings, no model, no network.

Most agent-memory tools record *everything* that happens and recall it by vector
similarity. `recoil` does the opposite on both axes:

- **It writes only on a flinch.** A memory is recorded when the development loop
  is *surprised* — a command errored, a test went red, a change got reverted, the
  user corrected you. Routine, unsurprising activity is not stored. Surprise is
  the encode gate, and it comes from signals you already have (`$?`, `git`), not
  from a model scoring text.
- **It recalls by the present situation, deterministically.** A memory is a *cue*
  (the files / error text / keywords it happened in) plus a *gist* (the lesson).
  Recall matches the cue against what you're doing *now* with plain keyword
  overlap — no embeddings — and fires the matching gists back. The situation
  reminds you; you don't go looking by name.

## Install

```sh
go build -o recoil .      # single static binary, stdlib only
```

## Use

```sh
recoil init

# remember something that surprised the loop
recoil encode --trigger test-fail \
  --gist "Don't name a Unity source folder Build/ — stock .gitignore untracks it." \
  --cue  "unity build folder gitignore untracked source"

# recall by the current situation (also reads piped stdin)
echo "editing the .gitignore and a new Build directory in the unity project" | recoil recall
recoil recall --situation "about to give the user a menu of options" --files src/foo.cs
```

Recall fires the most salient matching gists. Salience grows with the encoded
surprise weight and with how often a memory has been re-fired, so lessons that
keep proving relevant stay loud.

## Store

Plain TSV at `$RECOIL_DIR/store.tsv` (default `./.recoil/store.tsv`): one record
per line, `id  created  trigger  weight  hits  last  cue  gist`. Greppable by
design — the memory is text you can read and edit, not an opaque index.

## Triggers and default weights

| trigger      | weight | when |
|--------------|:------:|------|
| `correction` | 3.0    | the user said "no, that's wrong" |
| `revert`     | 2.5    | a change was reverted / rolled back |
| `test-fail`  | 2.0    | a test went red (then green) |
| `error`      | 1.5    | a command/tool errored |
| `manual`     | 1.0    | recorded by hand |

## Roadmap

- **Auto-capture** — a git hook + a command wrapper that detect the flinch
  (non-zero exit, `git revert`, red→green test, user correction) and call
  `recoil encode` with no manual step.
- **Decay** — memories never re-fired fade; surprise-born, re-fired ones persist.
- **Pre-action fire** — warn *before* repeating a known-bad action, not just on
  request.

## License

MIT.
