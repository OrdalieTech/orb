//go:build !js

package bedrock

import (
	"context"
	"net/http"

	"github.com/OrdalieTech/orb/ai/api"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
)

// awsChain resolves what the request options left open through the AWS
// shared config and credential chain: the profile's region, and credentials
// from the environment, shared files, SSO, a credential process, web identity
// or the container and instance metadata services.
func awsChain(ctx context.Context, config api.BedrockTransportConfig, client *http.Client) (string, func(context.Context) (api.BedrockCredentials, error), error) {
	options := []func(*awsconfig.LoadOptions) error{awsconfig.WithHTTPClient(client)}
	if config.Region != "" {
		options = append(options, awsconfig.WithRegion(config.Region))
	}
	if config.Profile != "" {
		options = append(options, awsconfig.WithSharedConfigProfile(config.Profile))
	}
	cfg, err := awsconfig.LoadDefaultConfig(ctx, options...)
	if err != nil {
		return "", nil, err
	}
	return cfg.Region, func(ctx context.Context) (api.BedrockCredentials, error) {
		value, err := cfg.Credentials.Retrieve(ctx)
		return api.BedrockCredentials{AccessKeyID: value.AccessKeyID, SecretAccessKey: value.SecretAccessKey, SessionToken: value.SessionToken}, err
	}, nil
}
