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
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/OrdalieTech/pigo/ai"
)

const (
	googleVertexCloudScope   = "https://www.googleapis.com/auth/cloud-platform"
	googleVertexTokenURL     = "https://oauth2.googleapis.com/token"
	googleVertexNoADCMessage = "Could not load the default credentials. Browse to https://cloud.google.com/docs/authentication/getting-started for more information."
)

var (
	googleVertexAuthHTTPClient       = http.DefaultClient
	googleVertexAuthNow              = time.Now
	googleVertexMetadataProbeTimeout = 3 * time.Second
	googleVertexAuthSleep            = func(ctx context.Context, duration time.Duration) error {
		timer := time.NewTimer(duration)
		defer timer.Stop()
		select {
		case <-timer.C:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	googleVertexUserHomeDir = os.UserHomeDir
)

type googleVertexADCFile struct {
	Type           string          `json:"type"`
	ClientID       string          `json:"client_id"`
	ClientSecret   string          `json:"client_secret"`
	RefreshToken   string          `json:"refresh_token"`
	ClientEmail    string          `json:"client_email"`
	PrivateKey     string          `json:"private_key"`
	QuotaProjectID string          `json:"quota_project_id"`
	Raw            json.RawMessage `json:"-"`
}

type googleVertexTokenResponse struct {
	AccessToken string `json:"access_token"`
	ExpiresIn   int64  `json:"expires_in"`
	TokenType   string `json:"token_type"`
}

type googleVertexADCHeaders struct {
	accessToken  string
	quotaProject string
}

type googleVertexADC struct {
	options *ai.StreamOptions
	client  *http.Client
	now     func() time.Time
	sleep   func(context.Context, time.Duration) error

	mu           sync.Mutex
	sourceLoaded bool
	credential   *googleVertexADCFile
	metadataBase string
	token        string
	expires      time.Time
}

func newGoogleVertexADC(options *ai.StreamOptions) *googleVertexADC {
	return &googleVertexADC{
		options: options,
		client:  googleVertexAuthHTTPClient,
		now:     googleVertexAuthNow,
		sleep:   googleVertexAuthSleep,
	}
}

func googleVertexAuthHeaders(ctx context.Context, options *ai.StreamOptions) (googleVertexADCHeaders, error) {
	return newGoogleVertexADC(options).headers(ctx)
}

func (adc *googleVertexADC) headers(ctx context.Context) (googleVertexADCHeaders, error) {
	adc.mu.Lock()
	defer adc.mu.Unlock()
	if adc.token != "" && adc.expires.After(adc.now().Add(5*time.Minute)) {
		return googleVertexADCHeaders{accessToken: adc.token, quotaProject: adc.quotaProject()}, nil
	}
	if !adc.sourceLoaded {
		if err := adc.loadSource(ctx); err != nil {
			return googleVertexADCHeaders{}, err
		}
	}
	response, err := adc.credentialToken(ctx, adc.credential)
	if err != nil {
		return googleVertexADCHeaders{}, err
	}
	if response.AccessToken == "" {
		return googleVertexADCHeaders{}, errors.New("Google application default credentials returned an empty access token") //nolint:staticcheck // Exact upstream text.
	}
	adc.token = response.AccessToken
	adc.expires = adc.now().Add(time.Duration(response.ExpiresIn) * time.Second)
	return googleVertexADCHeaders{accessToken: adc.token, quotaProject: adc.quotaProject()}, nil
}

func (adc *googleVertexADC) quotaProject() string {
	if override := adc.providerEnv("GOOGLE_CLOUD_QUOTA_PROJECT"); override != "" {
		return override
	}
	if adc.credential == nil {
		return ""
	}
	return adc.credential.QuotaProjectID
}

func (adc *googleVertexADC) loadSource(ctx context.Context) error {
	path := adc.providerEnv("GOOGLE_APPLICATION_CREDENTIALS")
	if path == "" {
		path = os.Getenv("google_application_credentials")
	}
	if path != "" {
		credential, err := readGoogleVertexADCFile(path)
		if err != nil {
			return err
		}
		adc.credential = credential
		adc.sourceLoaded = true
		return nil
	}
	home, err := googleVertexUserHomeDir()
	if err == nil && home != "" {
		path = filepath.Join(home, ".config", "gcloud", "application_default_credentials.json")
		if _, statErr := os.Stat(path); statErr == nil {
			credential, readErr := readGoogleVertexADCFile(path)
			if readErr != nil {
				return readErr
			}
			adc.credential = credential
			adc.sourceLoaded = true
			return nil
		} else if !errors.Is(statErr, os.ErrNotExist) {
			return statErr
		}
	}
	base := adc.providerEnv("GCE_METADATA_IP")
	if base == "" {
		base = adc.providerEnv("GCE_METADATA_HOST")
	}
	if base == "" {
		base = "169.254.169.254"
	}
	if !strings.HasPrefix(base, "http://") && !strings.HasPrefix(base, "https://") {
		base = "http://" + base
	}
	adc.metadataBase = strings.TrimRight(base, "/") + "/computeMetadata/v1"
	available, err := adc.metadataAvailable(ctx)
	if err != nil {
		return err
	}
	if !available {
		return errors.New(googleVertexNoADCMessage) //nolint:staticcheck // Exact upstream text.
	}
	adc.sourceLoaded = true
	return nil
}

func (adc *googleVertexADC) metadataAvailable(ctx context.Context) (bool, error) {
	// google-auth-library 10.6.2 computes checkIsGCE as
	// `getGCPResidency() || (await gcpMetadata.isAvailable())`, so GCP
	// residency short-circuits before METADATA_SERVER_DETECTION is
	// consulted, even under none/ping-only. (OT-m4)
	if adc.gcpResident() {
		return true, nil
	}
	detection := strings.ToLower(strings.TrimSpace(adc.providerEnv("METADATA_SERVER_DETECTION")))
	switch detection {
	case "assume-present":
		return true, nil
	case "none":
		return false, nil
	case "bios-only":
		return false, nil
	case "", "ping-only":
	default:
		return false, fmt.Errorf("Unknown `METADATA_SERVER_DETECTION` env variable. Got `%s`, but it should be `assume-present`, `none`, `bios-only`, `ping-only`, or unset", detection) //nolint:staticcheck // Exact upstream RangeError message.
	}
	// gcp-metadata isAvailable swallows every probe failure into "not
	// available", which surfaces the canonical no-credentials message. (OT-M8)
	if err := adc.probeMetadata(ctx); err != nil {
		return false, nil
	}
	return true, nil
}

func (adc *googleVertexADC) gcpResident() bool {
	if adc.providerEnv("CLOUD_RUN_JOB") != "" || adc.providerEnv("FUNCTION_NAME") != "" || adc.providerEnv("K_SERVICE") != "" {
		return true
	}
	if runtime.GOOS == "linux" {
		if _, err := os.Stat("/sys/class/dmi/id/bios_date"); err == nil {
			if vendor, err := os.ReadFile("/sys/class/dmi/id/bios_vendor"); err == nil && strings.Contains(string(vendor), "Google") {
				return true
			}
		}
	}
	interfaces, err := net.Interfaces()
	if err != nil {
		return false
	}
	for _, networkInterface := range interfaces {
		if strings.HasPrefix(strings.ToLower(networkInterface.HardwareAddr.String()), "42:01") {
			return true
		}
	}
	return false
}

func readGoogleVertexADCFile(path string) (*googleVertexADCFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read Google application default credentials %q: %w", path, err)
	}
	credential, err := decodeGoogleVertexADCFile(data)
	if err != nil {
		return nil, fmt.Errorf("decode Google application default credentials %q: %w", path, err)
	}
	return credential, nil
}

func decodeGoogleVertexADCFile(data []byte) (*googleVertexADCFile, error) {
	var credential googleVertexADCFile
	if err := json.Unmarshal(data, &credential); err != nil {
		return nil, err
	}
	credential.Raw = append(json.RawMessage(nil), data...)
	return &credential, nil
}

func (adc *googleVertexADC) credentialToken(ctx context.Context, credential *googleVertexADCFile) (googleVertexTokenResponse, error) {
	switch {
	case credential == nil:
		return adc.metadataToken(ctx)
	case credential.Type == "authorized_user":
		return adc.refreshAuthorizedUser(ctx, credential)
	case credential.Type == "external_account_authorized_user":
		return adc.externalAuthorizedUserToken(ctx, credential.Raw)
	case credential.Type == "impersonated_service_account":
		return adc.impersonatedServiceAccountToken(ctx, credential.Raw)
	case credential.Type == "external_account":
		return adc.externalAccountToken(ctx, credential.Raw)
	case credential.Type == "service_account":
		return adc.exchangeServiceAccountJWT(ctx, credential)
	case credential.ClientEmail != "" && credential.PrivateKey != "":
		return adc.exchangeServiceAccountJWT(ctx, credential)
	default:
		return googleVertexTokenResponse{}, fmt.Errorf("unsupported Google application default credential type %q", credential.Type)
	}
}

func (adc *googleVertexADC) providerEnv(name string) string {
	return providerEnvValue(name, adc.options)
}

func (adc *googleVertexADC) probeMetadata(ctx context.Context) error {
	probeCtx, cancel := context.WithTimeout(ctx, googleVertexMetadataProbeTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(probeCtx, http.MethodGet, adc.metadataBase+"/instance", nil)
	if err != nil {
		return err
	}
	request.Header.Set("Metadata-Flavor", "Google")
	response, err := adc.do(probeCtx, request)
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()
	_, _ = io.Copy(io.Discard, response.Body)
	return validateGoogleMetadataResponse(response)
}

func (adc *googleVertexADC) metadataToken(ctx context.Context) (googleVertexTokenResponse, error) {
	endpoint := adc.metadataBase + "/instance/service-accounts/default/token?scopes=" + url.QueryEscape(googleVertexCloudScope)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return googleVertexTokenResponse{}, err
	}
	request.Header.Set("Metadata-Flavor", "Google")
	response, err := adc.do(ctx, request)
	if err != nil {
		return googleVertexTokenResponse{}, err
	}
	defer func() { _ = response.Body.Close() }()
	if err := validateGoogleMetadataResponse(response); err != nil {
		return googleVertexTokenResponse{}, err
	}
	return decodeGoogleVertexToken(response.Body)
}

func validateGoogleMetadataResponse(response *http.Response) error {
	if response.Header.Get("Metadata-Flavor") != "Google" {
		value := response.Header.Get("Metadata-Flavor")
		if value == "" {
			value = "no header"
		} else {
			value = strconv.Quote(value)
		}
		return fmt.Errorf("invalid response from metadata service: incorrect Metadata-Flavor header: expected %q, got %s", "Google", value)
	}
	return nil
}

func (adc *googleVertexADC) refreshAuthorizedUser(ctx context.Context, credential *googleVertexADCFile) (googleVertexTokenResponse, error) {
	if credential.ClientID == "" || credential.ClientSecret == "" || credential.RefreshToken == "" {
		return googleVertexTokenResponse{}, errors.New("authorized_user ADC requires client_id, client_secret, and refresh_token")
	}
	return adc.postTokenForm(ctx,
		googleVertexFormValue{name: "refresh_token", value: credential.RefreshToken},
		googleVertexFormValue{name: "client_id", value: credential.ClientID},
		googleVertexFormValue{name: "client_secret", value: credential.ClientSecret},
		googleVertexFormValue{name: "grant_type", value: "refresh_token"},
	)
}

func (adc *googleVertexADC) exchangeServiceAccountJWT(ctx context.Context, credential *googleVertexADCFile) (googleVertexTokenResponse, error) {
	if credential.ClientEmail == "" || credential.PrivateKey == "" {
		return googleVertexTokenResponse{}, errors.New("service_account ADC requires client_email and private_key")
	}
	assertion, err := googleVertexJWT(credential.ClientEmail, credential.PrivateKey, adc.now())
	if err != nil {
		return googleVertexTokenResponse{}, err
	}
	return adc.postTokenForm(ctx,
		googleVertexFormValue{name: "grant_type", value: "urn:ietf:params:oauth:grant-type:jwt-bearer"},
		googleVertexFormValue{name: "assertion", value: assertion},
	)
}

func googleVertexJWT(email, privateKey string, now time.Time) (string, error) {
	block, _ := pem.Decode([]byte(privateKey))
	if block == nil {
		return "", errors.New("decode Google service account private key PEM")
	}
	var key *rsa.PrivateKey
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err == nil {
		var ok bool
		key, ok = parsed.(*rsa.PrivateKey)
		if !ok {
			return "", errors.New("Google service account private key is not RSA") //nolint:staticcheck // Exact upstream text.
		}
	} else {
		key, err = x509.ParsePKCS1PrivateKey(block.Bytes)
		if err != nil {
			return "", fmt.Errorf("parse Google service account private key: %w", err)
		}
	}
	header, err := ai.Marshal(struct {
		Algorithm string `json:"alg"`
	}{Algorithm: "RS256"})
	if err != nil {
		return "", err
	}
	iat := now.Unix()
	claims, err := ai.Marshal(struct {
		Issuer    string `json:"iss"`
		Scope     string `json:"scope"`
		Audience  string `json:"aud"`
		ExpiresAt int64  `json:"exp"`
		IssuedAt  int64  `json:"iat"`
	}{Issuer: email, Scope: googleVertexCloudScope, Audience: googleVertexTokenURL, ExpiresAt: iat + 3600, IssuedAt: iat})
	if err != nil {
		return "", err
	}
	encode := base64.RawURLEncoding.EncodeToString
	unsigned := encode(header) + "." + encode(claims)
	digest := sha256.Sum256([]byte(unsigned))
	signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		return "", err
	}
	return unsigned + "." + encode(signature), nil
}

type googleVertexFormValue struct {
	name  string
	value string
}

func googleVertexFormBody(values ...googleVertexFormValue) string {
	var body strings.Builder
	for index, value := range values {
		if index > 0 {
			body.WriteByte('&')
		}
		body.WriteString(googleVertexURLSearchParamsEscape(value.name))
		body.WriteByte('=')
		body.WriteString(googleVertexURLSearchParamsEscape(value.value))
	}
	return body.String()
}

func (adc *googleVertexADC) postTokenForm(ctx context.Context, values ...googleVertexFormValue) (googleVertexTokenResponse, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, googleVertexTokenURL, strings.NewReader(googleVertexFormBody(values...)))
	if err != nil {
		return googleVertexTokenResponse{}, err
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded;charset=UTF-8")
	response, err := adc.do(ctx, request)
	if err != nil {
		return googleVertexTokenResponse{}, err
	}
	defer func() { _ = response.Body.Close() }()
	return decodeGoogleVertexToken(response.Body)
}

func decodeGoogleVertexToken(reader io.Reader) (googleVertexTokenResponse, error) {
	var token googleVertexTokenResponse
	if err := json.NewDecoder(reader).Decode(&token); err != nil {
		return token, err
	}
	return token, nil
}

func (adc *googleVertexADC) do(ctx context.Context, request *http.Request) (*http.Response, error) {
	delays := [...]time.Duration{100 * time.Millisecond, 500 * time.Millisecond, 1500 * time.Millisecond}
	for attempt := 0; ; attempt++ {
		current := request
		if attempt > 0 {
			current = request.Clone(ctx)
			if request.GetBody != nil {
				body, err := request.GetBody()
				if err != nil {
					return nil, err
				}
				current.Body = body
			}
		}
		response, err := adc.client.Do(current)
		if err != nil {
			if ctx.Err() != nil || attempt >= 2 {
				return nil, err
			}
			if err := adc.sleep(ctx, delays[attempt]); err != nil {
				return nil, err
			}
			continue
		}
		if response.StatusCode >= http.StatusOK && response.StatusCode < http.StatusMultipleChoices {
			return response, nil
		}
		retry := response.StatusCode == http.StatusRequestTimeout || response.StatusCode == http.StatusTooManyRequests || response.StatusCode >= 500
		body, readErr := io.ReadAll(response.Body)
		_ = response.Body.Close()
		if readErr != nil {
			return nil, readErr
		}
		if !retry || attempt == len(delays) {
			return nil, fmt.Errorf("Google authentication request failed: %s: %s", response.Status, strings.TrimSpace(string(body))) //nolint:staticcheck // Exact upstream prefix.
		}
		if err := adc.sleep(ctx, delays[attempt]); err != nil {
			return nil, err
		}
	}
}
