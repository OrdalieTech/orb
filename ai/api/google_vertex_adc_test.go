package api

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/OrdalieTech/pigo/ai"
)

type googleVertexADCRoundTripFunc func(*http.Request) (*http.Response, error)

func (fn googleVertexADCRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func googleVertexADCTestClient(handler http.Handler) *http.Client {
	return &http.Client{Transport: googleVertexADCRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		response := recorder.Result()
		response.Request = request
		return response, nil
	})}
}

func writeGoogleVertexADCFile(t *testing.T, value any) string {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "application_default_credentials.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func useGoogleVertexADCTestClients(t *testing.T, authClient, vertexClient *http.Client) {
	t.Helper()
	oldAuthClient := googleVertexAuthHTTPClient
	oldVertexClient := googleHTTPClient
	googleVertexAuthHTTPClient = authClient
	googleHTTPClient = vertexClient
	t.Cleanup(func() {
		googleVertexAuthHTTPClient = oldAuthClient
		googleHTTPClient = oldVertexClient
	})
}

func isolateGoogleVertexADCEnvironment(t *testing.T) {
	t.Helper()
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", "")
	t.Setenv("google_application_credentials", "")
	t.Setenv("GCE_METADATA_IP", "")
	t.Setenv("GCE_METADATA_HOST", "")
	t.Setenv("METADATA_SERVER_DETECTION", "ping-only")
	t.Setenv("CLOUD_RUN_JOB", "")
	t.Setenv("FUNCTION_NAME", "")
	t.Setenv("K_SERVICE", "")
	oldHome := googleVertexUserHomeDir
	home := t.TempDir()
	googleVertexUserHomeDir = func() (string, error) { return home, nil }
	t.Cleanup(func() { googleVertexUserHomeDir = oldHome })
}

func TestGoogleVertexADCAuthorizedUserFormQuotaAndVertexHeaders(t *testing.T) {
	tokenRequest := make(chan url.Values, 1)
	tokenBody := make(chan string, 1)
	tokenHandler := http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			t.Errorf("token method = %s, want POST", request.Method)
		}
		if got := request.Header.Get("Content-Type"); !strings.HasPrefix(got, "application/x-www-form-urlencoded") {
			t.Errorf("token Content-Type = %q", got)
		}
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Errorf("read token form: %v", err)
		}
		tokenBody <- string(body)
		request.Body = io.NopCloser(strings.NewReader(string(body)))
		if err := request.ParseForm(); err != nil {
			t.Errorf("parse token form: %v", err)
		}
		tokenRequest <- request.PostForm
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, `{"access_token":"authorized-user-token","expires_in":3600,"token_type":"Bearer"}`)
	})

	vertexRequest := make(chan *http.Request, 1)
	vertexHandler := http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		clone := request.Clone(request.Context())
		clone.Body = nil
		vertexRequest <- clone
		writer.Header().Set("Content-Type", "text/event-stream")
		writer.WriteHeader(http.StatusOK)
	})

	credentialPath := writeGoogleVertexADCFile(t, googleVertexADCFile{
		Type:           "authorized_user",
		ClientID:       "client-id",
		ClientSecret:   "client-secret",
		RefreshToken:   "refresh-token",
		QuotaProjectID: "quota-project",
	})
	useGoogleVertexADCTestClients(t, googleVertexADCTestClient(tokenHandler), googleVertexADCTestClient(vertexHandler))

	options := &GoogleVertexOptions{
		StreamOptions: ai.StreamOptions{Env: ai.ProviderEnv{"GOOGLE_APPLICATION_CREDENTIALS": credentialPath}},
		Project:       "test-project",
		Location:      "us-central1",
	}
	response, err := postGoogleVertexStream(
		context.Background(),
		&ai.Model{ID: "gemini-3-flash-preview", Provider: "google-vertex", BaseURL: "https://vertex.example.test"},
		&options.StreamOptions,
		options,
		googleDecodedParameters{
			Model:    "gemini-3-flash-preview",
			Contents: json.RawMessage(`[{"role":"user","parts":[{"text":"hello"}]}]`),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()

	form := <-tokenRequest
	if got := <-tokenBody; got != "refresh_token=refresh-token&client_id=client-id&client_secret=client-secret&grant_type=refresh_token" {
		t.Errorf("token form bytes = %q", got)
	}
	if got := form.Get("client_id"); got != "client-id" {
		t.Errorf("client_id = %q", got)
	}
	if got := form.Get("client_secret"); got != "client-secret" {
		t.Errorf("client_secret = %q", got)
	}
	if got := form.Get("refresh_token"); got != "refresh-token" {
		t.Errorf("refresh_token = %q", got)
	}
	if got := form.Get("grant_type"); got != "refresh_token" {
		t.Errorf("grant_type = %q", got)
	}
	if got := form.Get("scope"); got != "" {
		t.Errorf("scope = %q, want omitted", got)
	}

	request := <-vertexRequest
	if got := request.Header.Get("Authorization"); got != "Bearer authorized-user-token" {
		t.Errorf("Authorization = %q", got)
	}
	if got := request.Header.Get("X-Goog-User-Project"); got != "quota-project" {
		t.Errorf("X-Goog-User-Project = %q", got)
	}
}

func TestGoogleVertexURLSearchParamsFormEncoding(t *testing.T) {
	got := googleVertexFormBody(
		googleVertexFormValue{name: "refresh_token", value: "a~ *é"},
		googleVertexFormValue{name: "grant_type", value: "refresh_token"},
	)
	if want := "refresh_token=a%7E+*%C3%A9&grant_type=refresh_token"; got != want {
		t.Fatalf("form = %q, want %q", got, want)
	}
}

func TestGoogleVertexADCServiceAccountJWTExchange(t *testing.T) {
	privateKey, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatal(err)
	}
	pkcs8, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	privateKeyPEM := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pkcs8}))
	credentialPath := writeGoogleVertexADCFile(t, googleVertexADCFile{
		Type:        "service_account",
		ClientEmail: "service-account@example.test",
		PrivateKey:  privateKeyPEM,
	})

	tokenRequest := make(chan url.Values, 1)
	tokenHandler := http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if got := request.URL.String(); got != googleVertexTokenURL {
			t.Errorf("token URL = %q, want %q", got, googleVertexTokenURL)
		}
		if err := request.ParseForm(); err != nil {
			t.Errorf("parse token form: %v", err)
		}
		tokenRequest <- request.PostForm
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, `{"access_token":"service-account-token","expires_in":3600,"token_type":"Bearer"}`)
	})

	client := googleVertexADCTestClient(tokenHandler)
	fixedNow := time.Unix(1_700_000_000, 0).UTC()
	adc := newGoogleVertexADC(&ai.StreamOptions{Env: ai.ProviderEnv{"GOOGLE_APPLICATION_CREDENTIALS": credentialPath}})
	adc.client = client
	adc.now = func() time.Time { return fixedNow }

	headers, err := adc.headers(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if headers.accessToken != "service-account-token" {
		t.Fatalf("access token = %q", headers.accessToken)
	}

	form := <-tokenRequest
	if got := form.Get("grant_type"); got != "urn:ietf:params:oauth:grant-type:jwt-bearer" {
		t.Errorf("grant_type = %q", got)
	}
	assertion := form.Get("assertion")
	parts := strings.Split(assertion, ".")
	if len(parts) != 3 {
		t.Fatalf("JWT has %d parts", len(parts))
	}
	decode := func(part string) []byte {
		value, decodeErr := base64.RawURLEncoding.DecodeString(part)
		if decodeErr != nil {
			t.Fatalf("decode JWT segment: %v", decodeErr)
		}
		return value
	}
	var header map[string]any
	if err := json.Unmarshal(decode(parts[0]), &header); err != nil {
		t.Fatal(err)
	}
	if len(header) != 1 || header["alg"] != "RS256" {
		t.Errorf("JWT header = %#v, want only alg=RS256", header)
	}
	if got := string(decode(parts[0])); got != `{"alg":"RS256"}` {
		t.Errorf("JWT header bytes = %s", got)
	}
	wantClaims := `{"iss":"service-account@example.test","scope":"https://www.googleapis.com/auth/cloud-platform","aud":"https://oauth2.googleapis.com/token","exp":1700003600,"iat":1700000000}`
	if got := string(decode(parts[1])); got != wantClaims {
		t.Errorf("JWT claim bytes = %s, want %s", got, wantClaims)
	}
	var claims struct {
		Issuer    string `json:"iss"`
		Scope     string `json:"scope"`
		Audience  string `json:"aud"`
		IssuedAt  int64  `json:"iat"`
		ExpiresAt int64  `json:"exp"`
	}
	if err := json.Unmarshal(decode(parts[1]), &claims); err != nil {
		t.Fatal(err)
	}
	if claims.Issuer != "service-account@example.test" || claims.Scope != googleVertexCloudScope || claims.Audience != googleVertexTokenURL {
		t.Errorf("JWT claims = %#v", claims)
	}
	if claims.IssuedAt != fixedNow.Unix() || claims.ExpiresAt != fixedNow.Unix()+3600 {
		t.Errorf("JWT times = iat %d exp %d", claims.IssuedAt, claims.ExpiresAt)
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(&privateKey.PublicKey, crypto.SHA256, digest[:], decode(parts[2])); err != nil {
		t.Fatalf("verify JWT signature: %v", err)
	}
}

func TestGoogleVertexADCMetadataProbeTokenAndHeaderValidation(t *testing.T) {
	t.Run("valid metadata source", func(t *testing.T) {
		isolateGoogleVertexADCEnvironment(t)
		requests := make(chan string, 2)
		handler := http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			requests <- request.URL.RequestURI()
			if got := request.Header.Get("Metadata-Flavor"); got != "Google" {
				t.Errorf("request Metadata-Flavor = %q", got)
			}
			writer.Header().Set("Metadata-Flavor", "Google")
			switch request.URL.Path {
			case "/computeMetadata/v1/instance":
				_, _ = io.WriteString(writer, `{}`)
			case "/computeMetadata/v1/instance/service-accounts/default/token":
				if got := request.URL.Query().Get("scopes"); got != googleVertexCloudScope {
					t.Errorf("metadata scope = %q", got)
				}
				writer.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(writer, `{"access_token":"metadata-token","expires_in":3600,"token_type":"Bearer"}`)
			default:
				http.NotFound(writer, request)
			}
		})

		adc := newGoogleVertexADC(&ai.StreamOptions{Env: ai.ProviderEnv{"GCE_METADATA_HOST": "metadata.example.test"}})
		adc.client = googleVertexADCTestClient(handler)
		headers, err := adc.headers(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if headers.accessToken != "metadata-token" || headers.quotaProject != "" {
			t.Errorf("metadata headers = %#v", headers)
		}
		if got := <-requests; got != "/computeMetadata/v1/instance" {
			t.Errorf("first metadata request = %q", got)
		}
		if got := <-requests; !strings.HasPrefix(got, "/computeMetadata/v1/instance/service-accounts/default/token?") {
			t.Errorf("second metadata request = %q", got)
		}
	})

	t.Run("probe requires response flavor", func(t *testing.T) {
		// gcp-metadata isAvailable swallows the flavor-validation failure, so
		// the canonical no-credentials message surfaces instead. (OT-M8)
		isolateGoogleVertexADCEnvironment(t)
		handler := http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(writer, `{}`)
		})
		adc := newGoogleVertexADC(&ai.StreamOptions{Env: ai.ProviderEnv{"GCE_METADATA_HOST": "metadata.example.test"}})
		adc.client = googleVertexADCTestClient(handler)
		_, err := adc.headers(context.Background())
		if err == nil || err.Error() != googleVertexNoADCMessage {
			t.Fatalf("error = %v, want %q", err, googleVertexNoADCMessage)
		}
	})

	t.Run("token requires response flavor", func(t *testing.T) {
		isolateGoogleVertexADCEnvironment(t)
		handler := http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			if request.URL.Path == "/computeMetadata/v1/instance" {
				writer.Header().Set("Metadata-Flavor", "Google")
				_, _ = io.WriteString(writer, `{}`)
				return
			}
			writer.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(writer, `{"access_token":"metadata-token","expires_in":3600}`)
		})
		adc := newGoogleVertexADC(&ai.StreamOptions{Env: ai.ProviderEnv{"GCE_METADATA_HOST": "metadata.example.test"}})
		adc.client = googleVertexADCTestClient(handler)
		_, err := adc.headers(context.Background())
		if err == nil || !strings.Contains(err.Error(), "incorrect Metadata-Flavor header") {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("probe uses the pinned off-GCP detection deadline", func(t *testing.T) {
		// The pinned probe deadline still fires (the fake transport only
		// returns once the context is done), but the timeout is swallowed
		// into "not available" and the canonical message surfaces. (OT-M8)
		isolateGoogleVertexADCEnvironment(t)
		oldTimeout := googleVertexMetadataProbeTimeout
		googleVertexMetadataProbeTimeout = 5 * time.Millisecond
		t.Cleanup(func() { googleVertexMetadataProbeTimeout = oldTimeout })
		adc := newGoogleVertexADC(&ai.StreamOptions{Env: ai.ProviderEnv{"GCE_METADATA_HOST": "metadata.example.test"}})
		adc.client = &http.Client{Transport: googleVertexADCRoundTripFunc(func(request *http.Request) (*http.Response, error) {
			<-request.Context().Done()
			return nil, request.Context().Err()
		})}
		_, err := adc.headers(context.Background())
		if err == nil || err.Error() != googleVertexNoADCMessage {
			t.Fatalf("metadata probe error = %v, want %q", err, googleVertexNoADCMessage)
		}
	})
}

func TestGoogleVertexADCMetadataDetectionModes(t *testing.T) {
	for _, test := range []struct {
		name      string
		detection string
		env       ai.ProviderEnv
		wantErr   string
	}{
		{name: "assume present", detection: "assume-present"},
		{name: "none", detection: "none", wantErr: googleVertexNoADCMessage},
		{name: "bios only on serverless", detection: "bios-only", env: ai.ProviderEnv{"K_SERVICE": "vertex-service"}},
		{name: "default on serverless", env: ai.ProviderEnv{"K_SERVICE": "vertex-service"}},
		{name: "unknown", detection: "surprise", wantErr: "unknown METADATA_SERVER_DETECTION value"},
		// google-auth-library 10.6.2 computes checkIsGCE as
		// `getGCPResidency() || (await gcpMetadata.isAvailable())`, so GCP
		// residency short-circuits before METADATA_SERVER_DETECTION is
		// consulted, even under none/ping-only. (OT-m4)
		{name: "none on serverless still resident OT-m4", detection: "none", env: ai.ProviderEnv{"K_SERVICE": "vertex-service"}},
		{name: "ping-only on serverless skips the probe OT-m4", detection: "ping-only", env: ai.ProviderEnv{"K_SERVICE": "vertex-service"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			isolateGoogleVertexADCEnvironment(t)
			env := ai.ProviderEnv{"GCE_METADATA_HOST": "metadata.example.test"}
			for key, value := range test.env {
				env[key] = value
			}
			if test.detection != "" {
				env["METADATA_SERVER_DETECTION"] = test.detection
			} else {
				t.Setenv("METADATA_SERVER_DETECTION", "")
			}
			var requests atomic.Int32
			adc := newGoogleVertexADC(&ai.StreamOptions{Env: env})
			adc.client = &http.Client{Transport: googleVertexADCRoundTripFunc(func(*http.Request) (*http.Response, error) {
				requests.Add(1)
				return nil, errors.New("unexpected metadata probe")
			})}
			err := adc.loadSource(context.Background())
			if test.wantErr == "" {
				if err != nil {
					t.Fatal(err)
				}
				if !adc.sourceLoaded {
					t.Fatal("metadata source was not loaded")
				}
			} else if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("error = %v, want substring %q", err, test.wantErr)
			}
			if got := requests.Load(); got != 0 {
				t.Fatalf("metadata probe requests = %d, want 0", got)
			}
		})
	}
}

// TestGoogleVertexADCUnreachableMetadataEmitsCanonicalMessage_OTM8 pins the
// upstream no-credentials failure: gcp-metadata isAvailable swallows probe
// failures (unreachable host, dial errors) into "not available", so with no
// other credential source loadSource emits the canonical Google message
// instead of surfacing the raw dial error. (OT-M8)
func TestGoogleVertexADCUnreachableMetadataEmitsCanonicalMessage_OTM8(t *testing.T) {
	isolateGoogleVertexADCEnvironment(t)
	adc := newGoogleVertexADC(&ai.StreamOptions{Env: ai.ProviderEnv{"GCE_METADATA_HOST": "metadata.example.test"}})
	adc.client = &http.Client{Transport: googleVertexADCRoundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("dial tcp 169.254.169.254:80: connect: network is unreachable")
	})}
	err := adc.loadSource(context.Background())
	if err == nil || err.Error() != googleVertexNoADCMessage {
		t.Fatalf("loadSource error = %v, want %q", err, googleVertexNoADCMessage)
	}
	if adc.sourceLoaded {
		t.Fatal("unreachable metadata server must not mark the source loaded")
	}
}

func TestGoogleVertexADCTokenCacheFiveMinuteBoundaryAndConcurrentRefresh(t *testing.T) {
	var requests atomic.Int32
	handler := http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		sequence := requests.Add(1)
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, `{"access_token":"token-`+strconv.FormatInt(int64(sequence), 10)+`","expires_in":3600}`)
	})

	start := time.Unix(1_700_000_000, 0).UTC()
	current := start
	adc := newGoogleVertexADC(nil)
	adc.client = googleVertexADCTestClient(handler)
	adc.now = func() time.Time { return current }
	adc.sourceLoaded = true
	adc.credential = &googleVertexADCFile{
		Type:         "authorized_user",
		ClientID:     "client-id",
		ClientSecret: "client-secret",
		RefreshToken: "refresh-token",
	}

	headers, err := adc.headers(context.Background())
	if err != nil || headers.accessToken != "token-1" {
		t.Fatalf("first headers = %#v, err = %v", headers, err)
	}
	current = start.Add(55*time.Minute - time.Nanosecond)
	headers, err = adc.headers(context.Background())
	if err != nil || headers.accessToken != "token-1" || requests.Load() != 1 {
		t.Fatalf("cached headers = %#v, requests = %d, err = %v", headers, requests.Load(), err)
	}

	current = start.Add(55 * time.Minute)
	headers, err = adc.headers(context.Background())
	if err != nil || headers.accessToken != "token-2" || requests.Load() != 2 {
		t.Fatalf("boundary headers = %#v, requests = %d, err = %v", headers, requests.Load(), err)
	}

	current = start.Add(110 * time.Minute)
	const callers = 20
	results := make(chan string, callers)
	errors := make(chan error, callers)
	var wait sync.WaitGroup
	for range callers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			resolved, resolveErr := adc.headers(context.Background())
			if resolveErr != nil {
				errors <- resolveErr
				return
			}
			results <- resolved.accessToken
		}()
	}
	wait.Wait()
	close(results)
	close(errors)
	for err := range errors {
		t.Errorf("concurrent refresh: %v", err)
	}
	for token := range results {
		if token != "token-3" {
			t.Errorf("concurrent token = %q", token)
		}
	}
	if got := requests.Load(); got != 3 {
		t.Errorf("token requests = %d, want 3", got)
	}
}

func TestGoogleVertexADCAuthRequestRetriesAndCancellation(t *testing.T) {
	newAuthorizedUser := func(client *http.Client) *googleVertexADC {
		adc := newGoogleVertexADC(nil)
		adc.client = client
		adc.sourceLoaded = true
		adc.credential = &googleVertexADCFile{
			Type:         "authorized_user",
			ClientID:     "client-id",
			ClientSecret: "client-secret",
			RefreshToken: "refresh-token",
		}
		return adc
	}

	t.Run("retryable statuses use pinned backoff", func(t *testing.T) {
		var attempts atomic.Int32
		handler := http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			switch attempts.Add(1) {
			case 1:
				http.Error(writer, "temporary", http.StatusInternalServerError)
			case 2:
				http.Error(writer, "limited", http.StatusTooManyRequests)
			case 3:
				http.Error(writer, "timeout", http.StatusRequestTimeout)
			default:
				writer.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(writer, `{"access_token":"retried-token","expires_in":3600}`)
			}
		})
		adc := newAuthorizedUser(googleVertexADCTestClient(handler))
		var delays []time.Duration
		adc.sleep = func(_ context.Context, delay time.Duration) error {
			delays = append(delays, delay)
			return nil
		}
		headers, err := adc.headers(context.Background())
		if err != nil || headers.accessToken != "retried-token" {
			t.Fatalf("headers = %#v, err = %v", headers, err)
		}
		wantDelays := []time.Duration{100 * time.Millisecond, 500 * time.Millisecond, 1500 * time.Millisecond}
		if !slices.Equal(delays, wantDelays) {
			t.Errorf("retry delays = %v, want %v", delays, wantDelays)
		}
		if got := attempts.Load(); got != 4 {
			t.Errorf("attempts = %d, want 4", got)
		}
	})

	t.Run("non-retryable response preserves status and body", func(t *testing.T) {
		var attempts atomic.Int32
		handler := http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			attempts.Add(1)
			http.Error(writer, "invalid credential", http.StatusBadRequest)
		})
		adc := newAuthorizedUser(googleVertexADCTestClient(handler))
		adc.sleep = func(context.Context, time.Duration) error {
			t.Fatal("non-retryable response slept")
			return nil
		}
		_, err := adc.headers(context.Background())
		if err == nil || !strings.Contains(err.Error(), "400 Bad Request") || !strings.Contains(err.Error(), "invalid credential") {
			t.Fatalf("error = %v", err)
		}
		if got := attempts.Load(); got != 1 {
			t.Errorf("attempts = %d, want 1", got)
		}
	})

	t.Run("cancellation interrupts retry backoff", func(t *testing.T) {
		var attempts atomic.Int32
		handler := http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			attempts.Add(1)
			http.Error(writer, "temporary", http.StatusInternalServerError)
		})
		adc := newAuthorizedUser(googleVertexADCTestClient(handler))
		ctx, cancel := context.WithCancel(context.Background())
		adc.sleep = func(ctx context.Context, _ time.Duration) error {
			cancel()
			return ctx.Err()
		}
		_, err := adc.headers(ctx)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v, want context canceled", err)
		}
		if got := attempts.Load(); got != 1 {
			t.Errorf("attempts = %d, want 1", got)
		}
	})

	t.Run("transport failures retry twice", func(t *testing.T) {
		var attempts atomic.Int32
		client := &http.Client{Transport: googleVertexADCRoundTripFunc(func(_ *http.Request) (*http.Response, error) {
			if attempts.Add(1) <= 2 {
				return nil, errors.New("temporary transport failure")
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Status:     "200 OK",
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(`{"access_token":"transport-retry-token","expires_in":3600}`)),
			}, nil
		})}
		adc := newAuthorizedUser(client)
		adc.sleep = func(context.Context, time.Duration) error { return nil }
		headers, err := adc.headers(context.Background())
		if err != nil {
			t.Fatalf("transport retry: %v", err)
		}
		if headers.accessToken != "transport-retry-token" || attempts.Load() != 3 {
			t.Errorf("headers = %#v, attempts = %d", headers, attempts.Load())
		}
	})
}

func TestGoogleVertexADCQuotaProjectEnvironmentOverride(t *testing.T) {
	adc := newGoogleVertexADC(&ai.StreamOptions{Env: ai.ProviderEnv{"GOOGLE_CLOUD_QUOTA_PROJECT": "environment-quota"}})
	adc.credential = &googleVertexADCFile{QuotaProjectID: "file-quota"}
	if got := adc.quotaProject(); got != "environment-quota" {
		t.Fatalf("quota project = %q, want environment override", got)
	}
	adc.options = &ai.StreamOptions{}
	if got := adc.quotaProject(); got != "file-quota" {
		t.Fatalf("quota project = %q, want credential file", got)
	}
	adc.credential = nil
	adc.options = &ai.StreamOptions{Env: ai.ProviderEnv{"GOOGLE_CLOUD_QUOTA_PROJECT": "metadata-quota"}}
	if got := adc.quotaProject(); got != "metadata-quota" {
		t.Fatalf("metadata quota project = %q", got)
	}
}

func TestGoogleVertexADCExplicitCredentialFileFailures(t *testing.T) {
	t.Run("missing explicit file does not fall back to metadata", func(t *testing.T) {
		isolateGoogleVertexADCEnvironment(t)
		var metadataRequests atomic.Int32
		handler := http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			metadataRequests.Add(1)
			writer.Header().Set("Metadata-Flavor", "Google")
			_, _ = io.WriteString(writer, `{}`)
		})
		missing := filepath.Join(t.TempDir(), "missing.json")
		adc := newGoogleVertexADC(&ai.StreamOptions{Env: ai.ProviderEnv{
			"GOOGLE_APPLICATION_CREDENTIALS": missing,
			"GCE_METADATA_HOST":              "metadata.example.test",
		}})
		adc.client = googleVertexADCTestClient(handler)
		_, err := adc.headers(context.Background())
		if err == nil || !strings.Contains(err.Error(), `read Google application default credentials "`+missing+`"`) {
			t.Fatalf("error = %v", err)
		}
		if got := metadataRequests.Load(); got != 0 {
			t.Errorf("metadata requests = %d, want 0", got)
		}
	})

	t.Run("malformed explicit file", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "malformed.json")
		if err := os.WriteFile(path, []byte(`{"type":`), 0o600); err != nil {
			t.Fatal(err)
		}
		adc := newGoogleVertexADC(&ai.StreamOptions{Env: ai.ProviderEnv{"GOOGLE_APPLICATION_CREDENTIALS": path}})
		_, err := adc.headers(context.Background())
		if err == nil || !strings.Contains(err.Error(), `decode Google application default credentials "`+path+`"`) {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("unsupported credential type", func(t *testing.T) {
		path := writeGoogleVertexADCFile(t, map[string]any{"type": "unsupported"})
		adc := newGoogleVertexADC(&ai.StreamOptions{Env: ai.ProviderEnv{"GOOGLE_APPLICATION_CREDENTIALS": path}})
		_, err := adc.headers(context.Background())
		if err == nil || !strings.Contains(err.Error(), `unsupported Google application default credential type "unsupported"`) {
			t.Fatalf("error = %v", err)
		}
	})

	for _, test := range []struct {
		name       string
		credential googleVertexADCFile
		message    string
	}{
		{
			name:       "incomplete authorized user",
			credential: googleVertexADCFile{Type: "authorized_user", ClientID: "client-id"},
			message:    "authorized_user ADC requires client_id, client_secret, and refresh_token",
		},
		{
			name:       "incomplete service account",
			credential: googleVertexADCFile{Type: "service_account", ClientEmail: "service@example.test"},
			message:    "service_account ADC requires client_email and private_key",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := writeGoogleVertexADCFile(t, test.credential)
			adc := newGoogleVertexADC(&ai.StreamOptions{Env: ai.ProviderEnv{"GOOGLE_APPLICATION_CREDENTIALS": path}})
			_, err := adc.headers(context.Background())
			if err == nil || err.Error() != test.message {
				t.Fatalf("error = %v, want %q", err, test.message)
			}
		})
	}
}
