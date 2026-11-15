package buzz

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
)

// The BIP-340 test vectors that carry a secret key
// (github.com/bitcoin/bips/blob/master/bip-0340/test-vectors.csv).
func TestSignMatchesBIP340Vectors(t *testing.T) {
	for _, vector := range [][4]string{
		{"0000000000000000000000000000000000000000000000000000000000000003", "F9308A019258C31049344F85F89D5229B531C845836F99B08601F113BCE036F9", "0000000000000000000000000000000000000000000000000000000000000000", "0000000000000000000000000000000000000000000000000000000000000000:E907831F80848D1069A5371B402410364BDF1C5F8307B0084C55F1CE2DCA821525F66A4A85EA8B71E482A74F382D2CE5EBEEE8FDB2172F477DF4900D310536C0"},
		{"B7E151628AED2A6ABF7158809CF4F3C762E7160F38B4DA56A784D9045190CFEF", "DFF1D77F2A671C5F36183726DB2341BE58FEAE1DA2DECED843240F7B502BA659", "0000000000000000000000000000000000000000000000000000000000000001", "243F6A8885A308D313198A2E03707344A4093822299F31D0082EFA98EC4E6C89:6896BD60EEAE296DB48A229FF71DFE071BDE413E6D43F917DC8DCF8C78DE33418906D11AC976ABCCB20B091292BFF4EA897EFCB639EA871CFA95F6DE339E4B0A"},
		{"C90FDAA22168C234C4C6628B80DC1CD129024E088A67CC74020BBEA63B14E5C9", "DD308AFEC5777E13121FA72B9CC1B7CC0139715309B086C960E18FD969774EB8", "C87AA53824B4D7AE2EB035A2B5BBBCCC080E76CDC6D1692C4B0B62D798E6D906", "7E2D58D8B3BCDF1ABADEC7829054F90DDA9805AAB56C77333024B9D0A508B75C:5831AAEED7B44BB74E5EAB94BA9D4294C49BCF2A60728D8B4C200F50DD313C1BAB745879A5AD954A72C45A91C3A51D3C7ADEA98D82F8481E0E1E03674A6F3FB7"},
		{"0B432B2677937381AEF05BB02A66ECD012773062CF3FA2549E44F58ED2401710", "25D1DFF95105F5253C4022F628A996AD3A0D95FBF21D468A1B33F8C160D8F517", "FFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFF", "FFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFF:7EB0509757E246F19449885651611CB965ECC1A187DD51B64FDA1EDC9637D5EC97582B9CB13DB3933705B32BA982AF5AF25FD78881EBB32771FC5922EFC66EA3"},
	} {
		k, err := parseKey(vector[0])
		if err != nil {
			t.Fatal(err)
		}
		if k.public != strings.ToLower(vector[1]) {
			t.Errorf("public key of %s: %s", vector[0], k.public)
		}
		message, signature, _ := strings.Cut(vector[3], ":")
		aux, _ := hex.DecodeString(vector[2])
		raw, _ := hex.DecodeString(message)
		if got := hex.EncodeToString(k.sign(raw, aux)); got != strings.ToLower(signature) {
			t.Errorf("signature with %s: %s", vector[0], got)
		}
	}
}

// NIP-19's example nsec.
func TestParseKeyReadsAnNsec(t *testing.T) {
	k, err := parseKey("nsec1vl029mgpspedva04g90vltkh6fvh240zqtv9k0t9af8935ke9laqsnlfe5")
	if err != nil {
		t.Fatal(err)
	}
	want, _ := parseKey("67dea2ed018072d675f5415ecfaed7d2597555e202d85b3d65ea4e58d2d92ffa")
	if k.public != want.public {
		t.Fatalf("nsec decodes to %s, want %s", k.public, want.public)
	}
	if _, err := parseKey("nsec1vl029mgpspedva04g90vltkh6fvh240zqtv9k0t9af8935ke9laqsnlfe4"); err == nil {
		t.Fatal("a bad checksum was accepted")
	}
}

// The id covers the event as relays serialize it: only JSON's mandatory
// escapes, so <, & and U+2028 stay as they are.
func TestEventIDUsesTheRelaysSerialization(t *testing.T) {
	k, _ := parseKey("0000000000000000000000000000000000000000000000000000000000000003")
	e := k.event(9, [][]string{{"h", "c1"}}, "a<b & \"c\"\n \x01")
	e.CreatedAt = 1700000000
	want := `[0,"f9308a019258c31049344f85f89d5229b531c845836f99b08601f113bce036f9",1700000000,9,[["h","c1"]],"a<b & \"c\"\n` + " " + `\u0001"]`
	if got := e.serialize(); got != want {
		t.Fatalf("serialized as\n%s\nwant\n%s", got, want)
	}
	signed := k.event(1, nil, "x")
	id := sha256.Sum256([]byte(signed.serialize()))
	if signed.ID != hex.EncodeToString(id[:]) || len(signed.Sig) != 128 || signed.Tags == nil {
		t.Fatalf("event %+v", signed)
	}
}

// The BIP-340 vectors verify, and a signature verifies for nothing else.
func TestVerifyMatchesBIP340Vectors(t *testing.T) {
	for _, vector := range [][3]string{
		{"F9308A019258C31049344F85F89D5229B531C845836F99B08601F113BCE036F9", "0000000000000000000000000000000000000000000000000000000000000000", "E907831F80848D1069A5371B402410364BDF1C5F8307B0084C55F1CE2DCA821525F66A4A85EA8B71E482A74F382D2CE5EBEEE8FDB2172F477DF4900D310536C0"},
		{"D69C3509BB99E412E68B0FE8544E72837DFA30746D8BE2AA65975F29D22DC7B9", "4DF3C3F68FCC83B27E9D42C90431A72499F17875C81A599B566C9889B9696703", "00000000000000000000003B78CE563F89A0ED9414F5AA28AD0D96D6795F9C6376AFB1548AF603B3EB45C9F8207DEE1060CB71C04E80F593060B07D28308D7F4"},
	} {
		public, _ := hex.DecodeString(vector[0])
		message, _ := hex.DecodeString(vector[1])
		signature, _ := hex.DecodeString(vector[2])
		if !verify(public, message, signature) {
			t.Errorf("%s does not verify", vector[2])
		}
		message[0] ^= 1
		if verify(public, message, signature) {
			t.Errorf("%s verifies another message", vector[2])
		}
	}
}

// An owner's attestation authorizes the agent it names, and no other key.
func TestAttestationAuthorizesItsAgentOnly(t *testing.T) {
	owner, _ := parseKey("B7E151628AED2A6ABF7158809CF4F3C762E7160F38B4DA56A784D9045190CFEF")
	agent, _ := parseKey(newKey())
	other, _ := parseKey(newKey())
	tag := owner.attest(agent.public)
	if err := checkAttestation(tag, agent.public); err != nil {
		t.Fatal(err)
	}
	if checkAttestation(tag, other.public) == nil {
		t.Fatal("an attestation authorized another key")
	}
}

// NIP-19's example npub.
func TestNpub(t *testing.T) {
	if got := npub("3bf0c63fcb93463407af97a5e5ee64fa883d107ef9e558472c4eb9aaaefa459d"); got != "npub180cvv07tjdrrgpa0j7j7tmnyl2yr6yr7l8j4s3evf6u64th6gkwsyjh6w6" {
		t.Fatalf("npub = %s", got)
	}
}
