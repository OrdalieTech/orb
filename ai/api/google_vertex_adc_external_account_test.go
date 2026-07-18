package api

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/OrdalieTech/pi-go/ai"
)

type googleVertexExternalAccountRoundTripFunc func(*http.Request) (*http.Response, error)

func (function googleVertexExternalAccountRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func googleVertexExternalAccountTestResponse(request *http.Request, body string) *http.Response {
	return &http.Response{
		Status: "200 OK", StatusCode: http.StatusOK, Header: make(http.Header),
		Body: io.NopCloser(strings.NewReader(body)), Request: request,
	}
}

func googleVertexExternalAccountRaw(t *testing.T, value any) json.RawMessage {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestGoogleVertexExternalAccountFileSTSWorkforceAndURLSearchParams(t *testing.T) {
	path := filepath.Join(t.TempDir(), "subject-token")
	if err := os.WriteFile(path, []byte("subject ~* token"), 0o600); err != nil {
		t.Fatal(err)
	}
	const audience = "//iam.googleapis.com/locations/global/workforcePools/pool/providers/provider"
	raw := googleVertexExternalAccountRaw(t, map[string]any{
		"type":                        "external_account",
		"audience":                    audience,
		"subject_token_type":          "urn:ietf:params:oauth:token-type:jwt",
		"token_url":                   "https://sts.example.test/v1/token",
		"scopes":                      []string{"credential-json-scope-is-ignored"},
		"workforce_pool_user_project": "billing-project",
		"credential_source":           map[string]any{"file": path},
	})

	var requestBody string
	adc := newGoogleVertexADC(nil)
	adc.client = &http.Client{Transport: googleVertexExternalAccountRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		requestBytes, err := io.ReadAll(request.Body)
		if err != nil {
			t.Fatal(err)
		}
		requestBody = string(requestBytes)
		if request.URL.String() != "https://sts.example.test/v1/token" {
			t.Errorf("STS URL = %q", request.URL.String())
		}
		if got := request.Header.Get("Content-Type"); got != "application/x-www-form-urlencoded;charset=UTF-8" {
			t.Errorf("STS Content-Type = %q", got)
		}
		if got := request.Header.Get("X-Goog-Api-Client"); !strings.Contains(got, "auth/10.6.2 google-byoid-sdk source/file sa-impersonation/false config-lifetime/false") {
			t.Errorf("metrics header = %q", got)
		}
		return googleVertexExternalAccountTestResponse(request, `{"access_token":"file-access","expires_in":3600,"token_type":"Bearer"}`), nil
	})}
	token, err := adc.externalAccountToken(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	if token.AccessToken != "file-access" || token.ExpiresIn != 3600 || token.TokenType != "Bearer" {
		t.Fatalf("token = %#v", token)
	}
	wantBody := "grant_type=urn%3Aietf%3Aparams%3Aoauth%3Agrant-type%3Atoken-exchange" +
		"&audience=%2F%2Fiam.googleapis.com%2Flocations%2Fglobal%2FworkforcePools%2Fpool%2Fproviders%2Fprovider" +
		"&scope=https%3A%2F%2Fwww.googleapis.com%2Fauth%2Fcloud-platform" +
		"&requested_token_type=urn%3Aietf%3Aparams%3Aoauth%3Atoken-type%3Aaccess_token" +
		"&subject_token=subject+%7E*+token" +
		"&subject_token_type=urn%3Aietf%3Aparams%3Aoauth%3Atoken-type%3Ajwt" +
		"&options=%7B%22userProject%22%3A%22billing-project%22%7D"
	if requestBody != wantBody {
		t.Errorf("STS body:\n got %s\nwant %s", requestBody, wantBody)
	}
}

func TestGoogleVertexExternalAccountURLClientAuthAndImpersonation(t *testing.T) {
	fixedNow := time.Date(2026, time.July, 18, 1, 2, 3, 0, time.UTC)
	raw := googleVertexExternalAccountRaw(t, map[string]any{
		"type":                              "external_account",
		"audience":                          "//iam.googleapis.com/locations/global/workforcePools/pool/providers/provider",
		"subject_token_type":                "urn:ietf:params:oauth:token-type:jwt",
		"token_url":                         "https://sts.example.test/v1/token",
		"client_id":                         "client-id",
		"client_secret":                     "client-secret",
		"workforce_pool_user_project":       "client-auth-takes-precedence",
		"scopes":                            []string{"scope~one", "scope*two"},
		"service_account_impersonation_url": "https://iam.example.test/v1/projects/-/serviceAccounts/service@example.test:generateAccessToken",
		"service_account_impersonation":     map[string]any{"token_lifetime_seconds": 1800},
		"credential_source": map[string]any{
			"url":     "https://identity.example.test/token",
			"headers": map[string]string{"Metadata": "value"},
			"format":  map[string]string{"type": "json", "subject_token_field_name": "token"},
		},
	})

	requests := make([]string, 0, 3)
	adc := newGoogleVertexADC(nil)
	adc.now = func() time.Time { return fixedNow }
	adc.client = &http.Client{Transport: googleVertexExternalAccountRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests = append(requests, request.URL.Host)
		switch request.URL.Host {
		case "identity.example.test":
			if request.Method != http.MethodGet || request.Header.Get("Metadata") != "value" {
				t.Errorf("identity request = %s headers %#v", request.Method, request.Header)
			}
			return googleVertexExternalAccountTestResponse(request, `{"token":"url-subject"}`), nil
		case "sts.example.test":
			body, _ := io.ReadAll(request.Body)
			if strings.Contains(string(body), "options=") {
				t.Errorf("client-auth STS body includes workforce options: %s", body)
			}
			if !strings.Contains(string(body), "scope=https%3A%2F%2Fwww.googleapis.com%2Fauth%2Fcloud-platform") {
				t.Errorf("impersonation STS scope body = %s", body)
			}
			wantBasic := "Basic " + base64.StdEncoding.EncodeToString([]byte("client-id:client-secret"))
			if request.Header.Get("Authorization") != wantBasic {
				t.Errorf("STS Authorization = %q", request.Header.Get("Authorization"))
			}
			return googleVertexExternalAccountTestResponse(request, `{"access_token":"federated-token","expires_in":600,"token_type":"Bearer"}`), nil
		case "iam.example.test":
			if request.Header.Get("Authorization") != "Bearer federated-token" {
				t.Errorf("IAM Authorization = %q", request.Header.Get("Authorization"))
			}
			body, _ := io.ReadAll(request.Body)
			if string(body) != `{"scope":["https://www.googleapis.com/auth/cloud-platform"],"lifetime":"1800s"}` {
				t.Errorf("IAM body = %s", body)
			}
			return googleVertexExternalAccountTestResponse(request, `{"accessToken":"impersonated-token","expireTime":"2026-07-18T01:32:03Z"}`), nil
		default:
			return nil, fmt.Errorf("unexpected host %s", request.URL.Host)
		}
	})}
	token, err := adc.externalAccountToken(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	if token.AccessToken != "impersonated-token" || token.ExpiresIn != 1800 {
		t.Errorf("token = %#v", token)
	}
	if strings.Join(requests, ",") != "identity.example.test,sts.example.test,iam.example.test" {
		t.Errorf("request order = %v", requests)
	}
}

func TestGoogleVertexExternalAccountExecutableSource(t *testing.T) {
	command := strconv.Quote(os.Args[0]) + " -test.run=^TestGoogleVertexExternalAccountExecutableHelper$"
	raw := googleVertexExternalAccountRaw(t, map[string]any{
		"type":               "external_account",
		"audience":           "//iam.googleapis.com/projects/123/locations/global/workloadIdentityPools/pool/providers/provider",
		"subject_token_type": "urn:ietf:params:oauth:token-type:jwt",
		"token_url":          "https://sts.example.test/v1/token",
		"credential_source": map[string]any{
			"executable": map[string]any{"command": command, "timeout_millis": 5000},
		},
	})
	options := &ai.StreamOptions{Env: ai.ProviderEnv{
		"GOOGLE_EXTERNAL_ACCOUNT_ALLOW_EXECUTABLES": "1",
		"PI_GO_ADC_EXECUTABLE_HELPER":               "1",
	}}
	adc := newGoogleVertexADC(options)
	adc.client = &http.Client{Transport: googleVertexExternalAccountRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		if err := request.ParseForm(); err != nil {
			t.Fatal(err)
		}
		if got := request.PostForm.Get("subject_token"); got != "executable-subject" {
			t.Errorf("subject token = %q", got)
		}
		if got := request.Header.Get("X-Goog-Api-Client"); !strings.Contains(got, "source/executable") {
			t.Errorf("metrics = %q", got)
		}
		return googleVertexExternalAccountTestResponse(request, `{"access_token":"executable-access","expires_in":3600}`), nil
	})}
	token, err := adc.externalAccountToken(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	if token.AccessToken != "executable-access" {
		t.Errorf("access token = %q", token.AccessToken)
	}
}

func TestGoogleVertexExternalAccountExecutableCommandParsing(t *testing.T) {
	cases := map[string][]string{
		`cmd "a b" c`: {"cmd", "a b", "c"},
		`cmd "abc`:    {"cmd", "abc"},
		`cmd a"b c"d`: {"cmd", `a"b c"d`},
		`"/a b" x`:    {"/a b", "x"},
		`cmd ""`:      {"cmd", ""},
	}
	for command, want := range cases {
		got, err := googleVertexExternalAccountParseCommand(command)
		if err != nil {
			t.Errorf("parse %q: %v", command, err)
			continue
		}
		if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
			t.Errorf("parse %q = %#v, want %#v", command, got, want)
		}
	}
}

func TestGoogleVertexExternalAccountExecutableHelper(t *testing.T) {
	if os.Getenv("PI_GO_ADC_EXECUTABLE_HELPER") != "1" {
		return
	}
	if os.Getenv("GOOGLE_EXTERNAL_ACCOUNT_AUDIENCE") == "" || os.Getenv("GOOGLE_EXTERNAL_ACCOUNT_INTERACTIVE") != "0" {
		_, _ = fmt.Fprint(os.Stderr, "missing external-account environment")
		os.Exit(2)
	}
	_, _ = fmt.Fprint(os.Stdout, `{"version":1,"success":true,"token_type":"urn:ietf:params:oauth:token-type:jwt","id_token":"executable-subject"}`)
	os.Exit(0)
}

func TestGoogleVertexExternalAccountAWSEnvironmentSigV4(t *testing.T) {
	fixedNow := time.Date(2023, time.November, 14, 22, 13, 20, 0, time.UTC)
	raw := googleVertexExternalAccountRaw(t, map[string]any{
		"type":               "external_account",
		"audience":           "//iam.googleapis.com/projects/123/locations/global/workloadIdentityPools/pool/providers/aws?<target>&value=>",
		"subject_token_type": "urn:ietf:params:aws:token-type:aws4_request",
		"token_url":          "https://sts.google.example.test/v1/token",
		"credential_source": map[string]any{
			"environment_id":                 "aws1",
			"regional_cred_verification_url": "https://sts.{region}.amazonaws.com?Action=GetCallerIdentity&Version=2011-06-15",
		},
	})
	options := &ai.StreamOptions{Env: ai.ProviderEnv{
		"AWS_REGION":            "us-east-2",
		"AWS_ACCESS_KEY_ID":     "AKIDEXAMPLE",
		"AWS_SECRET_ACCESS_KEY": "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY",
		"AWS_SESSION_TOKEN":     "session-token",
	}}
	adc := newGoogleVertexADC(options)
	adc.now = func() time.Time { return fixedNow }
	adc.client = &http.Client{Transport: googleVertexExternalAccountRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		if err := request.ParseForm(); err != nil {
			t.Fatal(err)
		}
		encoded := request.PostForm.Get("subject_token")
		serialized, err := url.QueryUnescape(encoded)
		if err != nil {
			t.Fatal(err)
		}
		var signed googleVertexExternalAccountAWSSignedRequest
		if err := json.Unmarshal([]byte(serialized), &signed); err != nil {
			t.Fatalf("decode AWS subject token %q: %v", serialized, err)
		}
		if strings.Contains(serialized, `\u0026`) || strings.Contains(serialized, `\u003c`) || strings.Contains(serialized, `\u003e`) ||
			!strings.Contains(serialized, "&Version=2011-06-15") || !strings.Contains(serialized, "aws?<target>&value=>") {
			t.Errorf("AWS signed request did not preserve JSON.stringify escaping: %s", serialized)
		}
		if signed.URL != "https://sts.us-east-2.amazonaws.com?Action=GetCallerIdentity&Version=2011-06-15" || signed.Method != http.MethodPost {
			t.Errorf("signed request = %#v", signed)
		}
		headers := make(map[string]string)
		for _, header := range signed.Headers {
			headers[header.Key] = header.Value
		}
		wantAuthorization := "AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/20231114/us-east-2/sts/aws4_request, SignedHeaders=host;x-amz-date;x-amz-security-token, Signature=e775b1ee5067879e927d0c6b2ca9c92b866730b92492a46472a751f4ff54f3c7"
		if headers["authorization"] != wantAuthorization {
			t.Errorf("AWS authorization:\n got %s\nwant %s", headers["authorization"], wantAuthorization)
		}
		if headers["x-amz-date"] != "20231114T221320Z" || headers["x-amz-security-token"] != "session-token" {
			t.Errorf("AWS headers = %#v", headers)
		}
		if headers["x-goog-cloud-target-resource"] == "" {
			t.Errorf("target resource header missing: %#v", headers)
		}
		return googleVertexExternalAccountTestResponse(request, `{"access_token":"aws-access","expires_in":3600}`), nil
	})}
	token, err := adc.externalAccountToken(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	if token.AccessToken != "aws-access" {
		t.Errorf("access token = %q", token.AccessToken)
	}
}

func TestGoogleVertexExternalAccountCertificateSource(t *testing.T) {
	fixedNow := time.Date(2026, time.July, 18, 0, 0, 0, 0, time.UTC)
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "workload"},
		NotBefore: fixedNow.Add(-time.Hour), NotAfter: fixedNow.Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &privateKey.PublicKey, privateKey)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	certPath := filepath.Join(directory, "leaf.pem")
	keyPath := filepath.Join(directory, "key.pem")
	configPath := filepath.Join(directory, "certificate-config.json")
	if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatal(err)
	}
	configData := googleVertexExternalAccountRaw(t, map[string]any{
		"cert_configs": map[string]any{"workload": map[string]string{"cert_path": certPath, "key_path": keyPath}},
	})
	if err := os.WriteFile(configPath, configData, 0o600); err != nil {
		t.Fatal(err)
	}
	certificateSource := &googleVertexExternalAccountCertificateSource{CertificateConfigLocation: configPath}
	mtlsADC := newGoogleVertexADC(nil)
	mtlsADC.client = &http.Client{Transport: &http.Transport{}}
	mtlsToken, err := mtlsADC.externalAccountCertificateSubjectToken(certificateSource)
	if err != nil {
		t.Fatal(err)
	}
	wantCertificateToken, _ := json.Marshal([]string{base64.StdEncoding.EncodeToString(der)})
	if mtlsToken != string(wantCertificateToken) {
		t.Errorf("direct certificate token = %s, want %s", mtlsToken, wantCertificateToken)
	}
	installedTransport, ok := mtlsADC.client.Transport.(*http.Transport)
	if !ok || installedTransport.TLSClientConfig == nil || len(installedTransport.TLSClientConfig.Certificates) != 1 {
		t.Fatalf("mTLS transport was not installed: %#v", mtlsADC.client.Transport)
	}
	raw := googleVertexExternalAccountRaw(t, map[string]any{
		"type":               "external_account",
		"audience":           "//iam.googleapis.com/projects/123/locations/global/workloadIdentityPools/pool/providers/certificate",
		"subject_token_type": "urn:ietf:params:oauth:token-type:jwt",
		"token_url":          "https://sts.example.test/v1/token",
		"credential_source": map[string]any{
			"certificate": map[string]any{"certificate_config_location": configPath},
		},
	})
	adc := newGoogleVertexADC(nil)
	adc.client = &http.Client{Transport: googleVertexExternalAccountRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		if err := request.ParseForm(); err != nil {
			t.Fatal(err)
		}
		if got := request.PostForm.Get("subject_token"); got != string(wantCertificateToken) {
			t.Errorf("certificate subject token:\n got %s\nwant %s", got, wantCertificateToken)
		}
		if got := request.Header.Get("X-Goog-Api-Client"); !strings.Contains(got, "source/certificate") {
			t.Errorf("metrics = %q", got)
		}
		return googleVertexExternalAccountTestResponse(request, `{"access_token":"certificate-access","expires_in":3600}`), nil
	})}
	token, err := adc.externalAccountToken(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	if token.AccessToken != "certificate-access" {
		t.Errorf("access token = %q", token.AccessToken)
	}
}
