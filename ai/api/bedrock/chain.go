//go:build !js

package bedrock

import (
	"context"
	"net/http"
	"sync"

	"github.com/OrdalieTech/orb/ai/api"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
)

type chainKey struct {
	region, profile string
	client          *http.Client
}

type chain struct {
	region      string
	credentials func(context.Context) (api.BedrockCredentials, error)
}

// chains keeps each loaded config, so its credential cache outlives the
// request that filled it.
var chains sync.Map

// awsChain resolves what the request options left open through the AWS
// shared config and credential chain: the profile's region, and credentials
// from the environment, shared files, SSO, a credential process, web identity
// or the container and instance metadata services.
func awsChain(ctx context.Context, config api.BedrockTransportConfig, client *http.Client) (string, func(context.Context) (api.BedrockCredentials, error), error) {
	key := chainKey{config.Region, config.Profile, client}
	cached, ok := chains.Load(key)
	if !ok {
		options := []func(*awsconfig.LoadOptions) error{awsconfig.WithHTTPClient(client)}
		if config.Region != "" {
			options = append(options, awsconfig.WithRegion(config.Region))
		}
		if config.Profile != "" {
			options = append(options, awsconfig.WithSharedConfigProfile(config.Profile))
		}
		loaded, err := awsconfig.LoadDefaultConfig(ctx, options...)
		if err != nil {
			return "", nil, err
		}
		cached, _ = chains.LoadOrStore(key, chain{loaded.Region, func(ctx context.Context) (api.BedrockCredentials, error) {
			value, err := loaded.Credentials.Retrieve(ctx)
			return api.BedrockCredentials{AccessKeyID: value.AccessKeyID, SecretAccessKey: value.SecretAccessKey, SessionToken: value.SessionToken}, err
		}})
	}
	resolved := cached.(chain)
	return resolved.region, resolved.credentials, nil
}
