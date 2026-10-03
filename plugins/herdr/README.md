# Herdr

Inside a Herdr pane, Orb's hidden adapter reports its interactive lifecycle.
Headless sessions never claim the pane.

When Herdr's managed pi integration loads successfully, it is the sole lifecycle
reporter (`herdr:pi`). Orb publishes only guarded `display_agent: Orb` metadata.
The existing interactive JS host starts with `HERDR_AGENT=pi`, allowing Herdr to
recognize that foreground job and validate `agent prompt`; the semantic API
identity is `pi`, while the displayed identity is Orb. The parent environment is
unchanged. On process handoff, Orb waits briefly for the matching pi session to
be acquired before publishing its metadata, then refreshes it on lifecycle
transitions.

If that integration is missing, disabled, fails to load, or lacks its socket
context, the native adapter remains the sole reporter (`custom:orb`, label
`orb`). Status reporting still works without JavaScript, but Herdr 0.9.1 does
not accept the custom Orb kind for `agent prompt`. Install/enable Herdr's pi
integration for validated prompting; no raw-input bypass is used.
