# Security Policy

pi-go is a coding agent that runs locally within the security boundary of the user running it, like
upstream pi. It executes shell commands and edits files at the user's direction; containing it
(container, VM, restricted user) is the user's responsibility. Credentials are stored in the agent
directory (`auth.json`) with owner-only permissions and are never transmitted anywhere except to
the provider they authenticate.

Report vulnerabilities privately to security@ordalie.com. Please include a reproduction; we aim to
acknowledge within 72 hours. For vulnerabilities in upstream pi itself, see the upstream repo's
SECURITY.md at github.com/earendil-works/pi.
