#!/usr/bin/env python3
"""Print a NIP-OA auth tag by which an owner authorizes an agent key on Buzz.

    python3 sign-auth-tag.py <agent pubkey hex>

Asks for the owner's secret key (nsec1… or hex) without echoing it and prints
the BUZZ_AUTH_TAG value: ["auth","<owner pubkey>","","<BIP-340 signature>"]
over SHA256("nostr:agent-auth:" + agent pubkey + ":"), no conditions. Python
standard library only; the key never leaves this process.
"""
import getpass
import hashlib
import json
import secrets
import sys

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


def main():
    if len(sys.argv) != 2 or len(sys.argv[1]) != 64:
        sys.exit("usage: sign-auth-tag.py <agent pubkey, 64 hex characters>")
    agent = sys.argv[1].lower()
    bytes.fromhex(agent)
    owner = getpass.getpass("Owner secret key (nsec1… or hex): ").strip()
    secret = bech32_secret(owner.lower()) if owner.lower().startswith("nsec1") else bytes.fromhex(owner)
    d = int.from_bytes(secret, "big")
    if not 0 < d < N:
        sys.exit("invalid secret key")
    public = mul(d)
    if public[1] % 2:
        d = N - d
    owner_x = public[0].to_bytes(32, "big")
    if owner_x.hex() == agent:
        sys.exit("the agent key must differ from the owner key")
    message = hashlib.sha256(f"nostr:agent-auth:{agent}:".encode()).digest()
    masked = (d ^ int.from_bytes(tagged("BIP0340/aux", secrets.token_bytes(32)), "big")).to_bytes(32, "big")
    k = int.from_bytes(tagged("BIP0340/nonce", masked + owner_x + message), "big") % N
    nonce = mul(k)
    if nonce[1] % 2:
        k = N - k
    r = nonce[0].to_bytes(32, "big")
    e = int.from_bytes(tagged("BIP0340/challenge", r + owner_x + message), "big") % N
    signature = r + ((k + e * d) % N).to_bytes(32, "big")
    print(json.dumps(["auth", owner_x.hex(), "", signature.hex()], separators=(",", ":")))


main()
