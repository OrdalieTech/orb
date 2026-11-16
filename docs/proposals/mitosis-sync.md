# Proposal — Mitosis profile sync (the "mitosis chain")

**Status:** owner-approved 2026-11-16 ("go, all authorizations, keep it simple"); the
cryptographic + gossip + orchestration core (§6 stages 1–5) is built fixtures-first and green on
branch `mitosis`. Stages 6–9 (daemon publish, credential-store wiring + CLI, auto-sync) remain.
The live-decision text in [§9](#9-decisionsmd-entry-to-adopt-on-sign-off) still needs to be signed
into `docs/DECISIONS.md` (that edit is the owner's, per the constitution).

**Author:** Claude Opus 4.8, 2026-11-16, on branch `mitosis`.

---

## 1. Problem

A Mitosis network is a set of Orb peers that reproduce across machines (laptop, server, Durable
Object, phone, browser). Today each peer is credential-islanded: to make a spawned peer able to
think you copy an `auth.json` or set provider-key env vars by hand. That is three bad things at
once — raw secrets copied in cleartext onto every substrate (grep-able, log-able, stealable by the
agent's own `bash`/`read` under prompt injection), OAuth refresh-token races between replicas
sharing one token, and manual toil on every `spawn`.

We want the opposite, and the owner stated the shape precisely:

- **No raw-value copying.** A peer receives the ability to decrypt secrets, never the secrets in a
  file anyone can cat.
- **Every peer fully autonomous.** Once admitted, a peer holds everything it needs locally and
  keeps working — and keeps admitting the next generation — with every other peer offline,
  including the laptop that spawned it. No central vault, no "my laptop is asleep so the edge peer
  is dead."
- **Automatic.** Pairing/spawning seals the shared secret to the new peer with zero extra commands.
- **Honest about the physical limit.** A peer that must boot unattended necessarily holds the means
  to decrypt its own secrets; an attacker with full disk/root on that machine can read them. This
  is accepted (it is true of every autonomous daemon: Tailscale, WireGuard, a systemd service with
  an API key). We defend everything short of that — accidental leakage, the agent reading its own
  keys, OAuth races — and we scope low-trust substrates to capped sub-keys.

This is iCloud Keychain's model (peer-to-peer, self-healing, per-device sealed) rebuilt over
Mitosis, carrying three payload classes: **credentials, configuration, shared memory.**

## 2. What we build on (already in the tree)

Nothing here is greenfield plumbing; it rides existing Mitosis (Bridge) seams:

- **Identity.** Every Orb already has an Ed25519 `PeerID` (`mitosis.Bridge`).
- **Signed, canonical, gossiped records.** `bridge/discovery.go` already defines
  `orb.peer-record/1`: JCS-canonical (`protocol.Canonical`), Ed25519-signed, `MaxRecord`-bounded
  (16 KiB), monotonic `Revision` counters, scope-membership-gated `PublishContact` / `MergeContact`
  / `Contacts`, with a free-form `Extensions map[string]json.RawMessage`.
- **The gossip loop.** `Peers.Reconcile()` already runs every 15 s and on `Bridge.Changes()`, and
  for each scope and member peer pulls `peers.list` and pushes `peers.publish`, merging by revision.
  The mitosis chain is a second record stream on exactly this loop.
- **Membership.** Scopes (`SetScope`, ≤128 peers) are the membership primitive. A sync group *is* a
  scope.
- **Durable, fenced storage.** `platforms/native/bridge/store.go` (flock, atomic replace, file +
  dir fsync) on native; `/bridge/` Store docs on Workers; both already persist `state.Records`,
  `state.Scopes`, `state.Blocked`, `state.Key`.
- **Secret hygiene for the plaintext end.** Tool processes already get only the `ORB_TOOL_ENV`
  allowlist and Orb marks itself non-dumpable; secrets already arrive on `ORB_SECRETS_FD`, never a
  path. The unsealed material lives behind that same boundary.

Consequence: the new surface is a record *schema* + a seal/merge *fold* + two optional RPC methods,
not a new transport, store, or identity system.

## 3. Design

### 3.1 Keys per peer

Add one static **X25519** keypair per Orb, beside its Ed25519 identity (stdlib `crypto/ecdh`,
available on go 1.27.3). The public half is published in the peer-record's existing `Extensions`
map under key `x25519` — additive, so 0.22 peers simply ignore it. The private half is stored
exactly where the Ed25519 key is (native flock'd Store; platform keystore on macOS/iOS/Android;
Worker Store doc), and never leaves the peer.

### 3.2 Profile Group Key (PGK) and epochs

A sync group (= a Mitosis profile = a scope) has a symmetric **Profile Group Key**, 256-bit, valid
for an **epoch** (a monotonic integer). Payloads are sealed with the PGK using AES-256-GCM (stdlib
`crypto/aes` + `crypto/cipher`, random 96-bit nonce per block, key rotated per epoch — well inside
GCM's safe-message bound). The PGK is never written in clear and never transits a record; it moves
only sealed to a specific peer (§3.4).

### 3.3 The log: signed, hash-linked blocks

One schema, `orb.mitosis-block/1`, a sibling of `orb.peer-record/1`:

```
Block {
  schema:     "orb.mitosis-block/1"
  scope_id:   <sync group>
  epoch:      <uint, decimal string>       // which PGK sealed the payload
  author:     <peer_id>                    // who signed this block
  seq:        <uint, decimal string>       // author's Lamport counter within (scope, epoch)
  prev:       [<hash>...]                  // heads this block extends (DAG; 0..8 parents)
  kind:       "member" | "cred" | "config" | "memory"
  nonce:      <base64url 12 bytes>         // AES-GCM nonce, absent for public member envelopes
  payload:    <base64url ciphertext>       // PGK-sealed, except member-envelope payloads (§3.4)
}
Record { payload: <canonical Block>, signature: <ed25519 over "orb.mitosis-block/1\n"+payload> }
```

`hash = SHA-256(canonical Block)`. Everything reuses the record machinery already written:
`protocol.Canonical` for JCS, `protocol.Counter` for monotonic fields, `MaxRecord` bound,
Ed25519 verify, `Blocked`/scope-membership gate. A block is accepted only if its signature
verifies, its `author` is a current scope member (or was, for already-stored history), it is
canonical, and it is within size. This is `MergeContact`'s exact check, extended to a DAG.

Blocks are gossiped on the existing `Peers.Reconcile` loop. Two optional negotiated methods carry
them (names under `orb-bridge/1`, so compat is preserved and old peers that don't advertise them
are simply not synced):

- `mitosis.blocks.heads` `{scope_id}` → current head hashes (what each side has).
- `mitosis.blocks.fetch` `{scope_id, have:[hash...]}` → the bounded page of blocks the caller is
  missing, newest-first, each ≤ `MaxRecord`, page ≤ `protocol.PageLimit`.

### 3.4 Payload classes

| kind | who may author | payload (sealed with PGK unless noted) | fold |
|---|---|---|---|
| `member` | owner only | **not PGK-sealed**: the epoch's PGK sealed *to one peer's X25519 key* (ephemeral-X25519 + HKDF-SHA256 + AES-256-GCM "sealed box"), plus the scope's member set at this epoch | admit/revoke + key handoff |
| `cred` | owner only | a credential: provider key, OAuth token set, or a capability handle; the `auth.json`/accounts shape, byte-for-byte | last-writer-wins per `(provider, account)` |
| `config` | owner only | a `settings.json` / `models.json` overlay fragment | last-writer-wins per key path |
| `memory` | any member | a `plugins/memory` delta: `append(item)`, `replace(id,item)`, `forget(id)` | union with tombstones |

Credential/config authorship is **owner-only** in v1 (the owner's Ed25519 key signs them), so a
spawned peer can *use* and *propagate* secrets but not *mint* new ones or rewrite config.
Delegated authorship (a signed "spawn voucher" letting a parent admit children and seal creds
within owner-set caps) is a deliberate later slice, not v1.

### 3.5 Enveloping — how a secret reaches a new peer with no raw copy

1. New peer P generates its X25519 keypair at first boot; its public key rides its peer-record.
2. On pairing/spawn/approve, the admitting owner peer writes a `member` block whose payload is the
   current-epoch PGK **sealed to P's X25519 public key**. Only P's private key opens it. Nothing on
   the wire or on any disk but P's is a usable key.
3. P syncs the log, opens its envelope → holds the PGK **in memory only** (behind the
   `ORB_TOOL_ENV`/non-dumpable boundary), unseals the `cred`/`config`/`memory` blocks, and is
   autonomous. It can now itself seal the PGK to the *next* peer's X25519 key (member blocks are
   owner-signed, so in v1 only the owner authors them; P relays them — the autonomy that matters,
   "keep working offline", holds regardless).

### 3.6 Merge / CRDT (deterministic, masterless)

Blocks form a DAG by `prev`. Peers exchange missing blocks by hash (`heads`/`fetch`) and fold
deterministically:

- **cred / config:** last-writer-wins keyed by the tuple `(epoch, seq, hash)` — higher epoch wins;
  then higher author `seq`; then higher `hash` as a total-order tiebreak. Concurrent owner edits
  on two machines converge to the same winner everywhere.
- **memory:** append is a union; `forget` writes a tombstone that suppresses an id; `replace` is a
  forget+append. Matches `plugins/memory` semantics and the existing "memory is the agent's, each
  session a view" decision.
- Convergence is independent of delivery order and of which subset of peers a given peer ever meets
  — meeting *one* up-to-date peer is enough to catch up (the user's "laptop opens at 9am and
  fast-forwards from whoever's online").

### 3.7 Autonomy and persistence

Each peer persists the full accepted log and its own X25519 private key in its existing Store, and
caches the unsealed PGK in memory. Nothing requires another peer to be reachable after admission.
On platforms with a hardware/OS keystore (macOS Keychain, iOS Secure Enclave, Android Keystore,
Cloudflare's encrypted DO storage) the X25519 private key is held there, so even disk theft does
not yield it; a plain Linux VPS keeps it in a 0600 Store file, like the Ed25519 key today.

### 3.8 Revocation

`orb mitosis block <peer>` (today's `orb bridge block`) bumps the epoch: the owner mints a fresh
PGK and writes `member` blocks sealing it to **every remaining peer only**. All subsequent
`cred`/`config`/`memory` blocks use the new epoch. The blocked peer keeps whatever it already
decrypted (unavoidable, §1) but receives nothing new and cannot decrypt new-epoch payloads. This
reuses the existing `block` + `Forget` path; it adds an epoch bump and a re-seal sweep.

### 3.9 Threat model (stated plainly)

- **Defended:** secrets never on disk in clear; the agent's own tools cannot read the keys
  (`ORB_TOOL_ENV`, non-dumpable, `ORB_SECRETS_FD`), so prompt injection cannot exfiltrate them;
  accidental leakage via logs/backups/`grep`; OAuth refresh races (owner is the single author of
  `cred` blocks, so one writer refreshes a token; peers consume); on keystore platforms, disk theft.
- **Explicitly not defended (physical limit):** an attacker with root/full-disk on an *autonomous*
  peer that boots unattended can recover that peer's X25519 key and decrypt its secrets. Mitigation
  is operational, not cryptographic: run low-trust substrates (a public browser tab, a throwaway
  sandbox) under a **separate profile** whose PGK seals only a capped sub-key (e.g. a $2
  OpenRouter key), never the personal profile.

### 3.10 Defaults (P4) and compatibility (P5/P11)

- **Inert by default.** Sync is off until a profile opts in; a profile carries only what the owner
  marks shareable. Matches P4 ("Bridge defaults are inert").
- **Additive on the wire.** New `x25519` extension field, new `orb.mitosis-block/1` schema, two new
  negotiated methods under the unchanged `orb-bridge/1` ALPN. A 0.22 peer ignores all of it and
  keeps pairing/driving as before. No on-disk format of today's Bridge changes.

## 4. Dependencies (§8)

Stdlib covers identity, sealing and hashing: `crypto/ecdh` (X25519), `crypto/ed25519`,
`crypto/aes` + `crypto/cipher` (AES-256-GCM), `crypto/sha256`. HKDF for the sealed-box KDF comes
from `golang.org/x/crypto/hkdf`, already in the module (`x/crypto v0.57.0`); adopting it promotes
one indirect dep to direct and adds a line to the §8 table. No new module. (If we prefer a single
AEAD everywhere, `x/crypto/chacha20poly1305` is likewise already present — decide at implementation;
stdlib AES-256-GCM is the default to honor "stdlib first".)

## 5. Fixtures-first plan (P8)

Kernel surfaces land their goldens red first. New Orb-owned fixture families (not pi conformance):

1. **Block canonicalization + signing** — fixed inputs → exact canonical bytes, hash, signature.
2. **Sealed box** — fixed X25519 keypairs + PGK → deterministic (with pinned ephemeral/nonce)
   envelope, and round-trip open.
3. **Merge fold** — scripted interleavings of `cred`/`config`/`memory` blocks across epochs and
   concurrent authors → one canonical converged state, order-independent.
4. **Revocation** — epoch bump re-seals to remaining members only; blocked peer cannot open new
   epoch.

These are golden Go tests under the mitosis package, outside `conformance/` (per the "Bridge
protocol tests are Orb-owned checks, separate from Pi conformance" rule).

## 6. Staged rollout (each stage green, P8)

Implementation note (2026-11-16): v1 took the simplest shape that meets §1 — a single
owner-published, per-epoch **vault document** riding the peer-record gossip, rather than a block
DAG. `vault.Build` generates a fresh group key and seals only to the scope's current members on
every publish, so key rotation and revocation are automatic: a peer removed from the scope simply
stops receiving the next group key, and no explicit epoch/block machinery is needed. The DAG,
`memory` class and `heads`/`fetch` methods from §3.3–§3.6 are deferred; they are not needed for
credential + config sync.

Done (branch `mitosis`, fixtures-first, all green; `bridge/vault` + `bridge`):

1. ✅ **Sealing primitives** (`bridge/vault/vault.go`): `X25519FromSeed`, `Seal`/`Open` sealed box,
   `NewGroupKey`/`SealGroup`/`OpenGroup`. Stdlib only. Known-answer wire goldens + property tests.
2. ✅ **Vault document** (`bridge/vault/document.go`): `Build`/`OpenBundle`, per-peer envelopes,
   bounded membership, revocation-by-reseal, JSON round-trip.
3. ✅ **Key advertisement + enumeration** (`bridge/discovery.go`): `Bridge.X25519Public`,
   `x25519` peer-record extension, `Bridge.ScopeMembers`.
4. ✅ **Gossip** (`bridge/discovery.go`, `bridge/bridge.go`): `Bridge.SetVault`/`ScopeVault`, the
   vault rides the owner's record, persisted and backward-compatible.
5. ✅ **Orchestration seam** (`bridge/vault/sync.go`): `vault.Publish`/`Apply`,
   `Bridge.OpenVault` (decrypts with the identity key, which never leaves the process).

6. ✅ **Credential merge** (`ai/auth/merge.go`): `MergeCredentials` (overlay wins per provider,
   never deletes a local-only login), `Store.Export`/`Store.MergeIn` (atomic).
7. ✅ **Daemon method + CLI**: `daemon.MitosisScope` (deterministic per-profile scope),
   `Service.mitosisSync` (join scope, advertise key, seal credentials, return this device's vault),
   and `orb mitosis sync [--push|--pull]` (`cmd/orb/mitosis_sync.go`) which exports the local auth
   store, has the daemon seal it, and merges back what is sealed to this device. Covered by an
   in-process two-bridge end-to-end test with real stores and a real daemon-method test (the
   sandbox blocks socket-level live daemons; the Go tests run the identical path).

Remaining:

8. **Automatic sync in `Peers.Reconcile`** (owner re-seals on credential/membership change; peers
   apply on receipt), so devices converge without running the command. v1 is the manual command.
9. **Config/models class** (currently credentials only) and per-profile opt-in + low-trust
   capped-profile guidance in the UI.
10. (Later) `memory` class; delegated spawn vouchers for non-owner authorship; a live two-machine
    acceptance test (needs an environment that permits a persistent daemon and the relay).

### What works today, end to end

`orb mitosis sync` on each device of a profile shares that device's stored credentials with the
others, sealed per device (X25519), merged without wiping local-only logins, and revocable by
removing a device from the scope (the next publish reseals only to the survivors). No raw key is
ever copied. It is eventually consistent over the reconcile loop; run it once per device to join.

## 7. What this unlocks

`orb login` once on machine one; thereafter every peer you (or an agent's `spawn`) brings up — ssh
box, Durable Object, phone, browser tab — comes alive already signed in, already holding shared
config and memory, needing no key file and no reachable parent, and stays working when every other
peer is offline. Revocation is one command. Low-trust substrates are one `--profile` away from a
capped blast radius.

## 8. Open questions for the owner

1. **AEAD choice:** stdlib AES-256-GCM (recommended, "stdlib first") vs `x/crypto` XChaCha20-Poly1305
   (bigger nonce, simpler reuse story). Either way no new module.
2. **v1 authorship:** owner-only `cred`/`config` (recommended) vs ship delegated spawn vouchers in v1.
3. **Memory in the chain by default?** Credentials+config are the clear win; sharing *all* memory
   across every substrate may be more than wanted by default — propose memory sync is per-profile
   opt-in even within an opted-in profile.
4. **Scope of "profile":** reuse the existing Bridge profile (`personal`, …) as the sync group 1:1
   (recommended) vs a separate "sync group" concept layered on scopes.

## 9. DECISIONS.md entry to adopt on sign-off

To be inserted under **Live decisions → ACP and team agents** (or a new "Mitosis sync" subsection),
replacing nothing:

> **Mitosis profile sync.** A Mitosis profile may carry an owner-opted-in, append-only, signed,
> hash-linked **block log** (`orb.mitosis-block/1`) over the existing peer-record gossip, sealing
> three payload classes — credentials, settings/model config, and shared memory — to the profile's
> peers with no raw-value copying. Each Orb holds a static X25519 key beside its Ed25519 identity
> (published additively in the peer-record `Extensions`); a per-epoch 256-bit Profile Group Key,
> sealed to each admitted peer's X25519 key, encrypts payloads with AES-256-GCM. Credential and
> config blocks are owner-signed only; memory blocks may be authored by any member; blocks merge by
> a deterministic `(epoch, seq, hash)` last-writer-wins for config/credentials and append-with-
> tombstones for memory, so peers converge masterless and each is fully autonomous offline after
> admission. `orb mitosis block` bumps the epoch and re-seals to remaining peers only. The wire
> stays `orb-bridge/1` (new schema, `x25519` extension and `mitosis.blocks.*` methods are additive
> and negotiated); sync is inert by default and per-profile opt-in; low-trust substrates use a
> separate profile sealing only a capped sub-key. A peer with root on an autonomous host can
> recover its own secrets — accepted as the physical cost of unattended autonomy, mitigated by
> platform keystores and profile scoping, not by cryptography. Owner, <date>.
