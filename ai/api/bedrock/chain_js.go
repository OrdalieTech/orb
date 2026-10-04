//go:build js

package bedrock

import (
	"context"
	"errors"
	"net/http"

	"github.com/OrdalieTech/orb/ai/api"
)

// awsChain has no shared config or metadata service to read on js/wasm, so
// the request options must carry the region and the credentials or token.
func awsChain(context.Context, api.BedrockTransportConfig, *http.Client) (string, func(context.Context) (api.BedrockCredentials, error), error) {
	return "", nil, errors.New("Bedrock on js/wasm needs AWS_ACCESS_KEY_ID and AWS_SECRET_ACCESS_KEY or AWS_BEARER_TOKEN_BEDROCK") //nolint:staticcheck // Provider error text.
}
