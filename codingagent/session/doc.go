// Package session stores coding-agent conversations as pi-compatible JSONL
// trees. It intentionally accepts legacy and partially malformed records on
// read because the upstream session manager does the same.
package session
