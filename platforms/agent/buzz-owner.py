#!/usr/bin/env python3
"""What an owner signs for a team agent on Buzz, with the owner's key, on the owner's machine.

    python3 buzz-owner.py <agent pubkey hex>
    python3 buzz-owner.py <agent pubkey hex> --relay wss://… --name Sales [--allow <pubkey>,…|--anyone]

Asks for the owner's secret key (nsec1… or hex) without echoing it and prints
the agent's BUZZ_AUTH_TAG: the NIP-OA tag ["auth","<owner pubkey>","","<BIP-340
signature>"] over SHA256("nostr:agent-auth:" + agent pubkey + ":"), no
conditions. With --relay it also publishes the owner's kind:30177 record of the
agent (its name and who it answers: the owner alone, --allow pubkeys, or
--anyone), which Buzz's agent directory requires. Python standard library only;
the key never leaves this process.
"""
import argparse
import base64
import getpass
import hashlib
import json
import secrets
import time
import urllib.error
import urllib.request

P = 2**256 - 2**32 - 977
N = 0xFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFEBAAEDCE6AF48A03BBFD25E8CD0364141
G = (0x79BE667EF9DCBBAC55A06295CE870B07029BFCDB2DCE28D959F2815B16F81798,
     0x483ADA7726A3C4655DA4FBFC0E1108A8FD17B448A68554199C47D08FFB10D4B8)


def add(a, b):
    if a is None:
        return b
    if b is None:
        return a
    if a[0] == b[0] and (a[1] + b[1]) % P == 0:
        return None
    if a == b:
        slope = 3 * a[0] * a[0] * pow(2 * a[1], -1, P) % P
    else:
        slope = (b[1] - a[1]) * pow(b[0] - a[0], -1, P) % P
    x = (slope * slope - a[0] - b[0]) % P
    return x, (slope * (a[0] - x) - a[1]) % P


def mul(k, point=G):
    result = None
    while k:
        if k & 1:
            result = add(result, point)
        point, k = add(point, point), k >> 1
    return result


def tagged(tag, data):
    digest = hashlib.sha256(tag.encode()).digest()
    return hashlib.sha256(digest + digest + data).digest()


def bech32_secret(text):
    charset = "qpzry9x8gf2tvdw0s3jn54khce6mua7l"
    data = [charset.index(c) for c in text[text.rindex("1") + 1:-6]]
    bits, value, out = 0, 0, bytearray()
    for group in data:
        value, bits = (value << 5) | group, bits + 5
        if bits >= 8:
            bits -= 8
            out.append((value >> bits) & 0xFF)
    return bytes(out)


def sign(d, public_x, message):
    masked = (d ^ int.from_bytes(tagged("BIP0340/aux", secrets.token_bytes(32)), "big")).to_bytes(32, "big")
    k = int.from_bytes(tagged("BIP0340/nonce", masked + public_x + message), "big") % N
    nonce = mul(k)
    if nonce[1] % 2:
        k = N - k
    r = nonce[0].to_bytes(32, "big")
    e = int.from_bytes(tagged("BIP0340/challenge", r + public_x + message), "big") % N
    return (r + ((k + e * d) % N).to_bytes(32, "big")).hex()


def event(d, public_x, kind, tags, content):
    created = int(time.time())
    serialized = json.dumps([0, public_x.hex(), created, kind, tags, content], separators=(",", ":"), ensure_ascii=False)
    digest = hashlib.sha256(serialized.encode()).digest()
    return {"id": digest.hex(), "pubkey": public_x.hex(), "created_at": created, "kind": kind,
            "tags": tags, "content": content, "sig": sign(d, public_x, digest)}


def publish(d, public_x, relay, signed):
    """POSTs an event to the relay's /events, authenticated by a NIP-98 event."""
    url = relay.replace("wss://", "https://", 1).replace("ws://", "http://", 1).rstrip("/") + "/events"
    body = json.dumps(signed, separators=(",", ":"), ensure_ascii=False).encode()
    auth = event(d, public_x, 27235, [["u", url], ["method", "POST"], ["nonce", secrets.token_hex(16)],
                                      ["payload", hashlib.sha256(body).hexdigest()]], "")
    header = "Nostr " + base64.b64encode(json.dumps(auth, separators=(",", ":")).encode()).decode()
    request = urllib.request.Request(url, body, {"Authorization": header, "Content-Type": "application/json"})
    try:
        with urllib.request.urlopen(request, timeout=30) as response:
            return response.read().decode()
    except urllib.error.HTTPError as error:
        raise SystemExit(f"the relay refused the record: {error.code} {error.read().decode()}")


def main():
    parser = argparse.ArgumentParser(description="Sign a team agent's owner records for Buzz.")
    parser.add_argument("agent", help="the agent's pubkey, 64 hex characters")
    parser.add_argument("--relay", help="publish the kind:30177 record to this relay")
    parser.add_argument("--name", help="the agent's name in the record")
    who = parser.add_mutually_exclusive_group()
    who.add_argument("--allow", help="comma-separated pubkeys the agent answers besides the owner")
    who.add_argument("--anyone", action="store_true", help="the agent answers anyone")
    options = parser.parse_args()
    agent = options.agent.lower()
    if len(agent) != 64 or len(bytes.fromhex(agent)) != 32:
        parser.error("the agent pubkey is 64 hex characters")
    if options.relay and not options.name:
        parser.error("--relay needs --name")
    allow = [key.strip().lower() for key in (options.allow or "").split(",") if key.strip()]
    if any(len(key) != 64 or len(bytes.fromhex(key)) != 32 for key in allow):
        parser.error("--allow takes 64 hex character pubkeys")
    owner = getpass.getpass("Owner secret key (nsec1… or hex): ").strip()
    secret = bech32_secret(owner.lower()) if owner.lower().startswith("nsec1") else bytes.fromhex(owner)
    d = int.from_bytes(secret, "big")
    if not 0 < d < N:
        raise SystemExit("invalid secret key")
    public = mul(d)
    if public[1] % 2:
        d = N - d
    owner_x = public[0].to_bytes(32, "big")
    if owner_x.hex() == agent:
        raise SystemExit("the agent key must differ from the owner key")
    tag = ["auth", owner_x.hex(), "", sign(d, owner_x, hashlib.sha256(f"nostr:agent-auth:{agent}:".encode()).digest())]
    print("BUZZ_AUTH_TAG=" + json.dumps(tag, separators=(",", ":")))
    if options.relay:
        record = {"name": options.name, "parallelism": 1,
                  "respond_to": "anyone" if options.anyone else "allowlist" if allow else "owner-only"}
        if allow:
            record["respond_to_allowlist"] = allow
        print(publish(d, owner_x, options.relay, event(d, owner_x, 30177, [["d", agent]], json.dumps(record))))


main()
