# Publishing

How recoil reaches the plugin directories. recoil is already packaged and passes
`claude plugin validate --strict`; the rest is one-time and needs your accounts.

## Claude Code

The repo is its own marketplace, so anyone can install it today:

```
/plugin marketplace add EclipseElips/recoil
/plugin install recoil@recoil
```

To get it into Anthropic's public **community** directory (installable as
`@claude-community`):

1. `claude plugin validate . --strict` passes — the review pipeline runs the
   same check. (Done.)
2. Submit through the Console form: <https://platform.claude.com/plugins/submit>
   (for individual authors). A Team/Enterprise org can use
   <https://claude.ai/admin-settings/directory/submissions/plugins/new> instead.
   Point it at this repo and pick the memory category.
3. After review + automated safety screening it's pinned in
   `anthropics/claude-plugins-community` and syncs nightly.

The curated `claude-plugins-official` marketplace is separate: Anthropic chooses
those at its discretion — there's no application form. The route in is to be in
the community directory and earn the "Anthropic Verified" badge.

## Codex

The repo ships a Codex plugin (`.codex-plugin/plugin.json`) plus the skill, so a
user can add it as a marketplace source or drop the skill into any repo (see the
README).

OpenAI's official `openai/plugins` catalog is curated — there's no documented
self-serve submission for a small community CLI yet. Until there is, the
self-hosted plugin + skill is the supported path.

## Once a directory lists it

A listed plugin's CLI can prompt users to install it (Claude Code "plugin
hints"). Not wired up yet — add it if/when recoil is accepted.
