# Vendored skills

`pulumi-*` are the official Pulumi agent skills, vendored from
https://github.com/pulumi/agent-skills (`pulumi` plugin) at commit `2517e28`.

Only the four skills relevant to EasyLab are vendored. The upstream plugin also
ships `pulumi-component`, `pulumi-esc`, `pulumi-context-api`,
`package-usage` and `provider-upgrade`, which this project does not use.

Note: upstream code examples are TypeScript. EasyLab's Pulumi programs are Go,
so treat the snippets as concepts, not as code to copy.

Licensed under Apache-2.0; see LICENSE-pulumi-agent-skills.
To update, re-copy from upstream rather than editing in place.
