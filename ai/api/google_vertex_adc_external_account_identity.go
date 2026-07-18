package api

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"

	"github.com/OrdalieTech/pi-go/ai"
)

type googleVertexExternalAccountCertificateConfig struct {
	CertConfigs struct {
		Workload struct {
			CertPath string `json:"cert_path"`
			KeyPath  string `json:"key_path"`
		} `json:"workload"`
	} `json:"cert_configs"`
}

func (adc *googleVertexADC) externalAccountIdentitySubjectToken(
	ctx context.Context,
	config *googleVertexExternalAccountConfig,
) (string, string, error) {
	source := &config.CredentialSource
	formatType := source.Format.Type
	if formatType == "" {
		formatType = "text"
	}
	if formatType != "text" && formatType != "json" {
		return "", "", fmt.Errorf("invalid credential_source format %q", formatType)
	}
	if formatType == "json" && source.Format.SubjectTokenFieldName == "" {
		return "", "", errors.New("missing subject_token_field_name for JSON credential_source format")
	}

	configured := 0
	if source.File != "" {
		configured++
	}
	if source.URL != "" {
		configured++
	}
	if source.Certificate != nil {
		configured++
	}
	if configured != 1 {
		return "", "", errors.New(`no valid Identity Pool "credential_source" provided, must be either file, url, or certificate`)
	}

	switch {
	case source.File != "":
		token, err := googleVertexExternalAccountFileSubjectToken(source.File, formatType, source.Format.SubjectTokenFieldName)
		return token, "file", err
	case source.URL != "":
		token, err := adc.externalAccountURLSubjectToken(ctx, source.URL, source.Headers, formatType, source.Format.SubjectTokenFieldName)
		return token, "url", err
	default:
		token, err := adc.externalAccountCertificateSubjectToken(source.Certificate)
		return token, "certificate", err
	}
}

func googleVertexExternalAccountFileSubjectToken(path, formatType, fieldName string) (string, error) {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", fmt.Errorf("the file at %s does not exist, or it is not a file: %w", path, err)
	}
	info, err := os.Lstat(resolved)
	if err != nil || !info.Mode().IsRegular() {
		if err == nil {
			err = errors.New("path is not a regular file")
		}
		return "", fmt.Errorf("the file at %s does not exist, or it is not a file: %w", resolved, err)
	}
	data, err := os.ReadFile(resolved)
	if err != nil {
		return "", err
	}
	return googleVertexExternalAccountParseSubjectToken(data, formatType, fieldName, "credential_source file")
}

func (adc *googleVertexADC) externalAccountURLSubjectToken(
	ctx context.Context,
	endpoint string,
	headers map[string]string,
	formatType string,
	fieldName string,
) (string, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", err
	}
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	response, err := adc.do(ctx, request)
	if err != nil {
		return "", err
	}
	defer func() { _ = response.Body.Close() }()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		return "", err
	}
	return googleVertexExternalAccountParseSubjectToken(data, formatType, fieldName, "credential_source URL")
}

func googleVertexExternalAccountParseSubjectToken(data []byte, formatType, fieldName, source string) (string, error) {
	var subjectToken string
	if formatType == "text" {
		subjectToken = string(data)
	} else {
		var object map[string]json.RawMessage
		if err := json.Unmarshal(data, &object); err != nil {
			return "", err
		}
		if value, ok := object[fieldName]; ok {
			_ = json.Unmarshal(value, &subjectToken)
		}
	}
	if subjectToken == "" {
		return "", fmt.Errorf("unable to parse the subject_token from the %s", source)
	}
	return subjectToken, nil
}

func (adc *googleVertexADC) externalAccountCertificateSubjectToken(
	source *googleVertexExternalAccountCertificateSource,
) (string, error) {
	if source == nil {
		return "", errors.New("missing certificate credential source")
	}
	if !source.UseDefaultCertificateConfig && source.CertificateConfigLocation == "" {
		return "", errors.New("either use_default_certificate_config must be true or certificate_config_location must be provided")
	}
	if source.UseDefaultCertificateConfig && source.CertificateConfigLocation != "" {
		return "", errors.New("both use_default_certificate_config and certificate_config_location cannot be provided")
	}
	configPath, err := adc.externalAccountCertificateConfigPath(source)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		return "", fmt.Errorf("failed to read certificate config file at %s: %w", configPath, err)
	}
	var certificateConfig googleVertexExternalAccountCertificateConfig
	if err := json.Unmarshal(data, &certificateConfig); err != nil {
		return "", fmt.Errorf("failed to parse certificate config from %s: %w", configPath, err)
	}
	certPath := certificateConfig.CertConfigs.Workload.CertPath
	keyPath := certificateConfig.CertConfigs.Workload.KeyPath
	if certPath == "" || keyPath == "" {
		return "", fmt.Errorf("certificate config file (%s) is missing required cert_path or key_path in the workload config", configPath)
	}
	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		return "", fmt.Errorf("failed to read certificate file at %s: %w", certPath, err)
	}
	leaf, err := googleVertexExternalAccountParseCertificate(certPEM)
	if err != nil {
		return "", fmt.Errorf("failed to read certificate file at %s: %w", certPath, err)
	}
	keyPEM, err := os.ReadFile(keyPath)
	if err != nil {
		return "", fmt.Errorf("failed to read private key file at %s: %w", keyPath, err)
	}
	certificate, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return "", fmt.Errorf("failed to read private key file at %s: %w", keyPath, err)
	}
	if err := adc.externalAccountInstallClientCertificate(certificate); err != nil {
		return "", err
	}
	chain := []*x509.Certificate{leaf}
	if source.TrustChainPath != "" {
		chain, err = googleVertexExternalAccountCertificateChain(leaf, source.TrustChainPath)
		if err != nil {
			return "", err
		}
	}
	encoded := make([]string, len(chain))
	for index, certificate := range chain {
		encoded[index] = base64.StdEncoding.EncodeToString(certificate.Raw)
	}
	token, err := ai.Marshal(encoded)
	if err != nil {
		return "", err
	}
	return string(token), nil
}

func (adc *googleVertexADC) externalAccountCertificateConfigPath(source *googleVertexExternalAccountCertificateSource) (string, error) {
	candidates := []struct {
		path  string
		label string
	}{
		{path: source.CertificateConfigLocation, label: "provided certificate config path"},
		{path: adc.providerEnv("GOOGLE_API_CERTIFICATE_CONFIG"), label: `path from environment variable "GOOGLE_API_CERTIFICATE_CONFIG"`},
	}
	for _, candidate := range candidates {
		if candidate.path == "" {
			continue
		}
		if googleVertexExternalAccountRegularFile(candidate.path) {
			return candidate.path, nil
		}
		return "", fmt.Errorf("%s is invalid: %s", candidate.label, candidate.path)
	}
	configDir := adc.providerEnv("CLOUDSDK_CONFIG")
	if configDir == "" {
		if runtime.GOOS == "windows" {
			configDir = filepath.Join(adc.providerEnv("APPDATA"), "gcloud")
		} else {
			home := adc.providerEnv("HOME")
			if home == "" {
				home, _ = googleVertexUserHomeDir()
			}
			configDir = filepath.Join(home, ".config", "gcloud")
		}
	}
	path := filepath.Join(configDir, "certificate_config.json")
	if googleVertexExternalAccountRegularFile(path) {
		return path, nil
	}
	return "", fmt.Errorf("could not find certificate configuration file at %s", path)
}

func googleVertexExternalAccountRegularFile(path string) bool {
	info, err := os.Lstat(path)
	return err == nil && info.Mode().IsRegular()
}

func googleVertexExternalAccountParseCertificate(data []byte) (*x509.Certificate, error) {
	if block, _ := pem.Decode(data); block != nil && block.Type == "CERTIFICATE" {
		return x509.ParseCertificate(block.Bytes)
	}
	return x509.ParseCertificate(data)
}

func googleVertexExternalAccountCertificateChain(leaf *x509.Certificate, path string) ([]*x509.Certificate, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to process certificate chain from %s: %w", path, err)
	}
	remaining := data
	chain := make([]*x509.Certificate, 0)
	for {
		block, rest := pem.Decode(remaining)
		if block == nil {
			break
		}
		remaining = rest
		if block.Type != "CERTIFICATE" {
			continue
		}
		certificate, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("failed to parse certificate at index %d in trust chain file %s: %w", len(chain), path, err)
		}
		chain = append(chain, certificate)
	}
	leafIndex := -1
	for index, certificate := range chain {
		if certificate.Equal(leaf) {
			leafIndex = index
			break
		}
	}
	switch leafIndex {
	case -1:
		return append([]*x509.Certificate{leaf}, chain...), nil
	case 0:
		return chain, nil
	default:
		return nil, fmt.Errorf("leaf certificate exists in the trust chain but is not the first entry (found at index %d)", leafIndex)
	}
}

func (adc *googleVertexADC) externalAccountInstallClientCertificate(certificate tls.Certificate) error {
	client := *adc.client
	var transport *http.Transport
	switch current := adc.client.Transport.(type) {
	case nil:
		base, ok := http.DefaultTransport.(*http.Transport)
		if !ok {
			return errors.New("default HTTP transport does not support client certificates")
		}
		transport = base.Clone()
	case *http.Transport:
		transport = current.Clone()
	default:
		// In-memory transports used by deterministic tests do not establish TLS.
		return nil
	}
	if transport.TLSClientConfig == nil {
		transport.TLSClientConfig = &tls.Config{}
	} else {
		transport.TLSClientConfig = transport.TLSClientConfig.Clone()
	}
	transport.TLSClientConfig.Certificates = append(transport.TLSClientConfig.Certificates, certificate)
	client.Transport = transport
	adc.client = &client
	return nil
}
