package buzz

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"math/big"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// secp256k1 and BIP-340: an agent signs its own events and checks its owner's
// attestation; the relay verifies the rest. math/big is variable-time; it only
// ever signs with the agent's key, or its owner's on the owner's own machine.
var (
	fieldP    = hexInt("FFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFEFFFFFC2F")
	orderN    = hexInt("FFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFEBAAEDCE6AF48A03BBFD25E8CD0364141")
	basePoint = point{hexInt("79BE667EF9DCBBAC55A06295CE870B07029BFCDB2DCE28D959F2815B16F81798"), hexInt("483ADA7726A3C4655DA4FBFC0E1108A8FD17B448A68554199C47D08FFB10D4B8")}
)

func hexInt(text string) *big.Int {
	value, _ := new(big.Int).SetString(text, 16)
	return value
}

// point is an affine point; a nil x is the point at infinity.
type point struct{ x, y *big.Int }

func (a point) add(b point) point {
	switch {
	case a.x == nil:
		return b
	case b.x == nil:
		return a
	}
	slope := new(big.Int)
	if a.x.Cmp(b.x) == 0 {
		if new(big.Int).Add(a.y, b.y).Mod(new(big.Int).Add(a.y, b.y), fieldP).Sign() == 0 {
			return point{}
		}
		// 3x² / 2y
		slope.Mul(a.x, a.x).Mul(slope, big.NewInt(3))
		slope.Mul(slope, new(big.Int).ModInverse(new(big.Int).Lsh(a.y, 1), fieldP))
	} else {
		slope.Sub(b.y, a.y)
		slope.Mul(slope, new(big.Int).ModInverse(new(big.Int).Sub(b.x, a.x).Mod(new(big.Int).Sub(b.x, a.x), fieldP), fieldP))
	}
	slope.Mod(slope, fieldP)
	x := new(big.Int).Mul(slope, slope)
	x.Sub(x, a.x).Sub(x, b.x).Mod(x, fieldP)
	y := new(big.Int).Sub(a.x, x)
	y.Mul(y, slope).Sub(y, a.y).Mod(y, fieldP)
	return point{x, y}
}

func (p point) times(k *big.Int) point {
	result, addend := point{}, p
	for i := range k.BitLen() {
		if k.Bit(i) == 1 {
			result = result.add(addend)
		}
		addend = addend.add(addend)
	}
	return result
}

func taggedHash(tag string, parts ...[]byte) []byte {
	prefix := sha256.Sum256([]byte(tag))
	hash := sha256.New()
	hash.Write(prefix[:])
	hash.Write(prefix[:])
	for _, part := range parts {
		hash.Write(part)
	}
	return hash.Sum(nil)
}

func bytes32(value *big.Int) []byte { return value.FillBytes(make([]byte, 32)) }

// key is an agent's secret, negated as BIP-340 requires when its point has
// an odd y, and its x-only public key.
type key struct {
	secret *big.Int
	public string
}

// parseKey reads a secret key as 64 hex characters or an nsec.
func parseKey(text string) (key, error) {
	text = strings.TrimSpace(text)
	raw, err := hex.DecodeString(text)
	if strings.HasPrefix(strings.ToLower(text), "nsec1") {
		raw, err = decodeBech32("nsec", text)
	}
	if err != nil || len(raw) != 32 {
		return key{}, errors.New("buzz: the private key is 64 hex characters or an nsec")
	}
	secret := new(big.Int).SetBytes(raw)
	if secret.Sign() == 0 || secret.Cmp(orderN) >= 0 {
		return key{}, errors.New("buzz: invalid private key")
	}
	public := basePoint.times(secret)
	if public.y.Bit(0) == 1 {
		secret.Sub(orderN, secret)
	}
	return key{secret: secret, public: hex.EncodeToString(bytes32(public.x))}, nil
}

// sign is the BIP-340 signature of message with auxiliary randomness aux.
func (k key) sign(message, aux []byte) []byte {
	public, _ := hex.DecodeString(k.public)
	masked := new(big.Int).SetBytes(taggedHash("BIP0340/aux", aux))
	masked.Xor(masked, k.secret)
	nonce := new(big.Int).SetBytes(taggedHash("BIP0340/nonce", bytes32(masked), public, message))
	nonce.Mod(nonce, orderN)
	commitment := basePoint.times(nonce)
	if commitment.y.Bit(0) == 1 {
		nonce.Sub(orderN, nonce)
	}
	challenge := new(big.Int).SetBytes(taggedHash("BIP0340/challenge", bytes32(commitment.x), public, message))
	challenge.Mul(challenge, k.secret).Add(challenge, nonce).Mod(challenge, orderN)
	return append(bytes32(commitment.x), bytes32(challenge)...)
}

// verify reports that signature is public's BIP-340 signature of message.
func verify(public, message, signature []byte) bool {
	x := new(big.Int).SetBytes(public)
	if len(public) != 32 || len(signature) != 64 || x.Cmp(fieldP) >= 0 {
		return false
	}
	// lift_x: y² = x³ + 7, the even root.
	square := new(big.Int).Exp(x, big.NewInt(3), fieldP)
	square.Add(square, big.NewInt(7)).Mod(square, fieldP)
	y := new(big.Int).Exp(square, new(big.Int).Rsh(new(big.Int).Add(fieldP, big.NewInt(1)), 2), fieldP)
	if new(big.Int).Exp(y, big.NewInt(2), fieldP).Cmp(square) != 0 {
		return false
	}
	if y.Bit(0) == 1 {
		y.Sub(fieldP, y)
	}
	r, s := new(big.Int).SetBytes(signature[:32]), new(big.Int).SetBytes(signature[32:])
	if r.Cmp(fieldP) >= 0 || s.Cmp(orderN) >= 0 {
		return false
	}
	challenge := new(big.Int).SetBytes(taggedHash("BIP0340/challenge", signature[:32], public, message))
	challenge.Sub(orderN, challenge.Mod(challenge, orderN))
	commitment := basePoint.times(s).add(point{x, y}.times(challenge))
	return commitment.x != nil && commitment.y.Bit(0) == 0 && commitment.x.Cmp(r) == 0
}

// newKey is a random secret key, as 64 hex characters.
func newKey() string {
	raw := make([]byte, 32)
	for {
		_, _ = rand.Read(raw)
		if value := new(big.Int).SetBytes(raw); value.Sign() > 0 && value.Cmp(orderN) < 0 {
			return hex.EncodeToString(raw)
		}
	}
}

// attestationDigest is what an owner signs for agent (NIP-OA, no conditions).
func attestationDigest(agent string) []byte {
	digest := sha256.Sum256([]byte("nostr:agent-auth:" + agent + ":"))
	return digest[:]
}

// attest is the owner's NIP-OA tag authorizing agent.
func (k key) attest(agent string) []string {
	aux := make([]byte, 32)
	_, _ = rand.Read(aux)
	return []string{"auth", k.public, "", hex.EncodeToString(k.sign(attestationDigest(agent), aux))}
}

// checkAttestation fails unless tag is an owner's NIP-OA tag, without
// conditions, authorizing agent.
func checkAttestation(tag []string, agent string) error {
	if len(tag) != 4 || tag[0] != "auth" || !isHex32(tag[1]) {
		return errors.New(`buzz: the auth tag is the owner's NIP-OA tag, ["auth","<owner pubkey>","","<signature>"]`)
	}
	public, _ := hex.DecodeString(tag[1])
	signature, err := hex.DecodeString(tag[3])
	if tag[2] != "" || err != nil || !verify(public, attestationDigest(agent), signature) {
		return errors.New("buzz: the auth tag does not authorize this agent's key")
	}
	return nil
}

// event is a signed Nostr event.
type event struct {
	ID        string     `json:"id"`
	PubKey    string     `json:"pubkey"`
	CreatedAt int64      `json:"created_at"`
	Kind      int        `json:"kind"`
	Tags      [][]string `json:"tags"`
	Content   string     `json:"content"`
	Sig       string     `json:"sig"`
}

// tag returns the value of the event's first tag named name, or "".
func (e event) tag(name string) string {
	for _, tag := range e.Tags {
		if len(tag) > 1 && tag[0] == name {
			return tag[1]
		}
	}
	return ""
}

// event signs an event of kind as k, now.
func (k key) event(kind int, tags [][]string, content string) event {
	if tags == nil {
		tags = [][]string{}
	}
	e := event{PubKey: k.public, CreatedAt: time.Now().Unix(), Kind: kind, Tags: tags, Content: content}
	id := sha256.Sum256([]byte(e.serialize()))
	aux := make([]byte, 32)
	_, _ = rand.Read(aux)
	e.ID, e.Sig = hex.EncodeToString(id[:]), hex.EncodeToString(k.sign(id[:], aux))
	return e
}

// serialize is NIP-01's [0,pubkey,created_at,kind,tags,content], escaped as
// relays escape it (serde_json): encoding/json would also escape <, >, & and
// U+2028/U+2029, and the relay would reject the id.
func (e event) serialize() string {
	var out strings.Builder
	out.WriteString(`[0,"` + e.PubKey + `",` + strconv.FormatInt(e.CreatedAt, 10) + "," + strconv.Itoa(e.Kind) + ",[")
	for i, tag := range e.Tags {
		if i > 0 {
			out.WriteByte(',')
		}
		out.WriteByte('[')
		for j, value := range tag {
			if j > 0 {
				out.WriteByte(',')
			}
			quote(&out, value)
		}
		out.WriteByte(']')
	}
	out.WriteString("],")
	quote(&out, e.Content)
	out.WriteByte(']')
	return out.String()
}

func quote(out *strings.Builder, text string) {
	out.WriteByte('"')
	for _, r := range strings.ToValidUTF8(text, string(utf8.RuneError)) {
		switch r {
		case '"':
			out.WriteString(`\"`)
		case '\\':
			out.WriteString(`\\`)
		case '\n':
			out.WriteString(`\n`)
		case '\r':
			out.WriteString(`\r`)
		case '\t':
			out.WriteString(`\t`)
		case '\b':
			out.WriteString(`\b`)
		case '\f':
			out.WriteString(`\f`)
		default:
			if r < 0x20 {
				out.WriteString(`\u00` + hex.EncodeToString([]byte{byte(r)}))
			} else {
				out.WriteRune(r)
			}
		}
	}
	out.WriteByte('"')
}

const bech32Charset = "qpzry9x8gf2tvdw0s3jn54khce6mua7l"

// npub is pubkey as NIP-19 shows it.
func npub(pubkey string) string {
	raw, _ := hex.DecodeString(pubkey)
	var values []int
	acc, bits := 0, 0
	for _, b := range raw {
		acc, bits = acc<<8|int(b), bits+8
		for bits >= 5 {
			bits -= 5
			values = append(values, acc>>bits&31)
		}
	}
	if bits > 0 {
		values = append(values, acc<<(5-bits)&31)
	}
	checked := append(hrpValues("npub"), values...)
	check := polymod(append(checked, 0, 0, 0, 0, 0, 0)) ^ 1
	out := []byte("npub1")
	for _, value := range values {
		out = append(out, bech32Charset[value])
	}
	for i := range 6 {
		out = append(out, bech32Charset[check>>(5*(5-i))&31])
	}
	return string(out)
}

func hrpValues(hrp string) []int {
	values := make([]int, 0, 2*len(hrp)+1)
	for _, c := range hrp {
		values = append(values, int(c)>>5)
	}
	values = append(values, 0)
	for _, c := range hrp {
		values = append(values, int(c)&31)
	}
	return values
}

// decodeBech32 returns the bytes of a bech32 string with prefix hrp, its
// checksum verified.
func decodeBech32(hrp, text string) ([]byte, error) {
	text = strings.ToLower(text)
	at := strings.LastIndexByte(text, '1')
	if at < 0 || text[:at] != hrp || len(text)-at < 7 {
		return nil, errors.New("not a " + hrp)
	}
	values := make([]int, 0, len(text)-at-1)
	for _, c := range text[at+1:] {
		value := strings.IndexRune(bech32Charset, c)
		if value < 0 {
			return nil, errors.New("not a " + hrp)
		}
		values = append(values, value)
	}
	if polymod(append(hrpValues(hrp), values...)) != 1 {
		return nil, errors.New("bad " + hrp + " checksum")
	}
	var out []byte
	bits, acc := 0, 0
	for _, value := range values[:len(values)-6] {
		acc, bits = acc<<5|value, bits+5
		if bits >= 8 {
			bits -= 8
			out = append(out, byte(acc>>bits))
		}
	}
	return out, nil
}

func polymod(values []int) int {
	generators := [...]int{0x3b6a57b2, 0x26508e6d, 0x1ea119fa, 0x3d4233dd, 0x2a1462b3}
	check := 1
	for _, value := range values {
		top := check >> 25
		check = (check&0x1ffffff)<<5 ^ value
		for i, generator := range generators {
			if top>>i&1 == 1 {
				check ^= generator
			}
		}
	}
	return check
}
