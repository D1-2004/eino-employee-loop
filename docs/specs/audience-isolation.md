# Context audience isolation

Owner identity does not grant permission to share evidence between scenes. The
default Context policy admits only evidence with an identical source and target
scope, for Front, Execution and Maintenance alike. Scene names do not encode
whether a conversation is private or a group.

A host may inject `contextbuilder.AudiencePolicy` through
`WithAudiencePolicy`. It receives the exact source scope, target scope and view.
The host must explicitly authorize each cross-scene transfer. Workspace, tenant,
employee and requester boundaries remain mandatory even with a custom policy.
`CheckAudience` is also available to owner workers that reload original evidence
outside `Build`.

Before exposing an Owner Markdown projection, Build verifies its authoritative
metadata, content digest, epoch, all source revisions and quote bindings, and the
audience of every cited source. Any unavailable or unauthorized evidence excludes
the entire document. Missing metadata or an empty evidence list is not permission
to expose a raw Markdown file. Gaps reveal no source content or scene identity.

Frozen frames retain a projection digest and its scoped evidence. Revalidate
checks the policy again before replay, including after a host revokes a former
cross-scene grant. Legacy frozen frames containing Owner files without those
bindings fail closed and must be rebuilt. Ordinary projection updates do not
invalidate an already frozen authorized projection; source revocation and owner
reset still do. This policy is an exposure boundary, not a prompt instruction.
