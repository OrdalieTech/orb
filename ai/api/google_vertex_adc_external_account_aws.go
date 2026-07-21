package api

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/OrdalieTech/pigo/ai"
)

type googleVertexExternalAccountAWSCredentials struct {
	AccessKeyID     string
	SecretAccessKey string
	SessionToken    string
}

type googleVertexExternalAccountAWSMetadataCredentials struct {
	AccessKeyID     string `json:"AccessKeyId"`
	SecretAccessKey string `json:"SecretAccessKey"`
	SessionToken    string `json:"Token"`
}

type googleVertexExternalAccountAWSHeader struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

type googleVertexExternalAccountAWSSignedRequest struct {
	URL     string                                 `json:"url"`
	Method  string                                 `json:"method"`
	Headers []googleVertexExternalAccountAWSHeader `json:"headers"`
}

func (adc *googleVertexADC) externalAccountAWSSubjectToken(
	ctx context.Context,
	config *googleVertexExternalAccountConfig,
) (string, error) {
	source := &config.CredentialSource
	version := strings.TrimPrefix(source.EnvironmentID, "aws")
	if version == source.EnvironmentID || version == "" || source.RegionalCredVerificationURL == "" {
		return "", errors.New(`no valid AWS "credential_source" provided`)
	}
	parsedVersion, err := strconv.Atoi(version)
	if err != nil || parsedVersion != 1 {
		return "", fmt.Errorf("aws version %q is not supported in the current build", version)
	}
	region, err := adc.externalAccountAWSRegion(ctx, source)
	if err != nil {
		return "", err
	}
	credentials, err := adc.externalAccountAWSCredentials(ctx, source)
	if err != nil {
		return "", err
	}
	verificationURL := strings.Replace(source.RegionalCredVerificationURL, "{region}", region, 1)
	signed, err := googleVertexExternalAccountAWSSignRequest(verificationURL, region, credentials, adc.now())
	if err != nil {
		return "", err
	}
	signed.Headers = append(signed.Headers, googleVertexExternalAccountAWSHeader{
		Key: "x-goog-cloud-target-resource", Value: config.Audience,
	})
	sort.Slice(signed.Headers, func(left, right int) bool { return signed.Headers[left].Key < signed.Headers[right].Key })
	serialized, err := ai.Marshal(signed)
	if err != nil {
		return "", err
	}
	return googleVertexExternalAccountEncodeURIComponent(serialized), nil
}

func (adc *googleVertexADC) externalAccountAWSRegion(
	ctx context.Context,
	source *googleVertexExternalAccountCredentialSource,
) (string, error) {
	if region := adc.providerEnv("AWS_REGION"); region != "" {
		return region, nil
	}
	if region := adc.providerEnv("AWS_DEFAULT_REGION"); region != "" {
		return region, nil
	}
	if source.RegionURL == "" {
		return "", errors.New(`unable to determine AWS region due to missing credential_source.region_url`)
	}
	headers, err := adc.externalAccountAWSMetadataHeaders(ctx, source)
	if err != nil {
		return "", err
	}
	data, err := adc.externalAccountAWSMetadataRequest(ctx, http.MethodGet, source.RegionURL, headers)
	if err != nil {
		return "", err
	}
	if len(data) == 0 {
		return "", errors.New("AWS region metadata returned an empty availability zone") //nolint:staticcheck // Exact upstream text.
	}
	return string(data[:len(data)-1]), nil
}

func (adc *googleVertexADC) externalAccountAWSCredentials(
	ctx context.Context,
	source *googleVertexExternalAccountCredentialSource,
) (googleVertexExternalAccountAWSCredentials, error) {
	accessKeyID := adc.providerEnv("AWS_ACCESS_KEY_ID")
	secretAccessKey := adc.providerEnv("AWS_SECRET_ACCESS_KEY")
	if accessKeyID != "" && secretAccessKey != "" {
		return googleVertexExternalAccountAWSCredentials{
			AccessKeyID: accessKeyID, SecretAccessKey: secretAccessKey, SessionToken: adc.providerEnv("AWS_SESSION_TOKEN"),
		}, nil
	}
	if source.URL == "" {
		return googleVertexExternalAccountAWSCredentials{}, errors.New(`unable to determine AWS role name due to missing credential_source.url`)
	}
	headers, err := adc.externalAccountAWSMetadataHeaders(ctx, source)
	if err != nil {
		return googleVertexExternalAccountAWSCredentials{}, err
	}
	role, err := adc.externalAccountAWSMetadataRequest(ctx, http.MethodGet, source.URL, headers)
	if err != nil {
		return googleVertexExternalAccountAWSCredentials{}, err
	}
	data, err := adc.externalAccountAWSMetadataRequest(ctx, http.MethodGet, source.URL+"/"+string(role), headers)
	if err != nil {
		return googleVertexExternalAccountAWSCredentials{}, err
	}
	var response googleVertexExternalAccountAWSMetadataCredentials
	if err := json.Unmarshal(data, &response); err != nil {
		return googleVertexExternalAccountAWSCredentials{}, err
	}
	return googleVertexExternalAccountAWSCredentials(response), nil
}

func (adc *googleVertexADC) externalAccountAWSMetadataHeaders(
	ctx context.Context,
	source *googleVertexExternalAccountCredentialSource,
) (http.Header, error) {
	headers := make(http.Header)
	if source.IMDSv2SessionTokenURL == "" {
		return headers, nil
	}
	token, err := adc.externalAccountAWSMetadataRequest(ctx, http.MethodPut, source.IMDSv2SessionTokenURL, http.Header{
		"X-Aws-Ec2-Metadata-Token-Ttl-Seconds": {"300"},
	})
	if err != nil {
		return nil, err
	}
	headers.Set("X-Aws-Ec2-Metadata-Token", string(token))
	return headers, nil
}

func (adc *googleVertexADC) externalAccountAWSMetadataRequest(
	ctx context.Context,
	method string,
	endpoint string,
	headers http.Header,
) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, method, endpoint, nil)
	if err != nil {
		return nil, err
	}
	request.Header = headers.Clone()
	response, err := adc.do(ctx, request)
	if err != nil {
		return nil, err
	}
	defer func() { _ = response.Body.Close() }()
	return io.ReadAll(response.Body)
}

func googleVertexExternalAccountAWSSignRequest(
	endpoint string,
	region string,
	credentials googleVertexExternalAccountAWSCredentials,
	now time.Time,
) (googleVertexExternalAccountAWSSignedRequest, error) {
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return googleVertexExternalAccountAWSSignedRequest{}, err
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return googleVertexExternalAccountAWSSignedRequest{}, errors.New("AWS regional credential verification URL must be absolute") //nolint:staticcheck // Exact upstream text.
	}
	host := googleVertexExternalAccountAWSHost(parsed)
	service := strings.Split(host, ".")[0]
	amzDate := now.UTC().Format("20060102T150405Z")
	dateStamp := now.UTC().Format("20060102")
	headers := map[string]string{
		"host":       host,
		"x-amz-date": amzDate,
	}
	if credentials.SessionToken != "" {
		headers["x-amz-security-token"] = strings.Join(strings.Fields(credentials.SessionToken), " ")
	}
	headerNames := make([]string, 0, len(headers))
	for name := range headers {
		headerNames = append(headerNames, name)
	}
	sort.Strings(headerNames)
	var canonicalHeaders strings.Builder
	for _, name := range headerNames {
		canonicalHeaders.WriteString(name)
		canonicalHeaders.WriteByte(':')
		canonicalHeaders.WriteString(strings.Join(strings.Fields(headers[name]), " "))
		canonicalHeaders.WriteByte('\n')
	}
	signedHeaders := strings.Join(headerNames, ";")
	path := parsed.EscapedPath()
	if path == "" {
		path = "/"
	}
	emptyHash := sha256.Sum256(nil)
	canonicalRequest := "POST\n" + path + "\n" + parsed.RawQuery + "\n" + canonicalHeaders.String() + "\n" + signedHeaders + "\n" + hex.EncodeToString(emptyHash[:])
	credentialScope := dateStamp + "/" + region + "/" + service + "/aws4_request"
	canonicalHash := sha256.Sum256([]byte(canonicalRequest))
	stringToSign := "AWS4-HMAC-SHA256\n" + amzDate + "\n" + credentialScope + "\n" + hex.EncodeToString(canonicalHash[:])
	signingKey := googleVertexExternalAccountAWSHMAC([]byte("AWS4"+credentials.SecretAccessKey), dateStamp)
	signingKey = googleVertexExternalAccountAWSHMAC(signingKey, region)
	signingKey = googleVertexExternalAccountAWSHMAC(signingKey, service)
	signingKey = googleVertexExternalAccountAWSHMAC(signingKey, "aws4_request")
	signature := hex.EncodeToString(googleVertexExternalAccountAWSHMAC(signingKey, stringToSign))
	authorization := "AWS4-HMAC-SHA256 Credential=" + credentials.AccessKeyID + "/" + credentialScope +
		", SignedHeaders=" + signedHeaders + ", Signature=" + signature
	headers["authorization"] = authorization

	names := make([]string, 0, len(headers))
	for name := range headers {
		names = append(names, name)
	}
	sort.Strings(names)
	serializedHeaders := make([]googleVertexExternalAccountAWSHeader, 0, len(names))
	for _, name := range names {
		serializedHeaders = append(serializedHeaders, googleVertexExternalAccountAWSHeader{Key: name, Value: headers[name]})
	}
	return googleVertexExternalAccountAWSSignedRequest{URL: endpoint, Method: http.MethodPost, Headers: serializedHeaders}, nil
}

func googleVertexExternalAccountAWSHost(parsed *url.URL) string {
	host := parsed.Host
	if hostname, port, err := net.SplitHostPort(parsed.Host); err == nil {
		if (parsed.Scheme == "https" && port == "443") || (parsed.Scheme == "http" && port == "80") {
			return hostname
		}
	}
	return host
}

func googleVertexExternalAccountAWSHMAC(key []byte, value string) []byte {
	hash := hmac.New(sha256.New, key)
	_, _ = hash.Write([]byte(value))
	return hash.Sum(nil)
}

func googleVertexExternalAccountEncodeURIComponent(value []byte) string {
	const hexadecimal = "0123456789ABCDEF"
	var encoded strings.Builder
	for _, character := range value {
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') || strings.ContainsRune("-_.!~*'()", rune(character)) {
			encoded.WriteByte(character)
			continue
		}
		encoded.WriteByte('%')
		encoded.WriteByte(hexadecimal[character>>4])
		encoded.WriteByte(hexadecimal[character&15])
	}
	return encoded.String()
}
