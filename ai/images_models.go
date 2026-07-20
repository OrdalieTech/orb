package ai

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/OrdalieTech/pi-go/ai/auth"
)

type ModelsError = auth.Error

const (
	ModelsErrorModelSource     = auth.ErrorModelSource
	ModelsErrorModelValidation = auth.ErrorModelValidation
	ModelsErrorProvider        = auth.ErrorProvider
	ModelsErrorStream          = auth.ErrorStream
	ModelsErrorAuth            = auth.ErrorAuth
	ModelsErrorOAuth           = auth.ErrorOAuth
)

type ImagesProvider interface {
	ID() ImagesProviderID
	Name() string
	Auth() auth.ProviderAuth
	GetModels() ([]ImagesModel, error)
	GenerateImages(context.Context, ImagesRequest) (*AssistantImages, error)
}

type refreshableImagesProvider interface {
	RefreshModels(context.Context) error
}

type ImagesModels interface {
	GetProviders() []ImagesProvider
	GetProvider(ImagesProviderID) ImagesProvider
	GetModels(...ImagesProviderID) []ImagesModel
	GetModel(ImagesProviderID, string) *ImagesModel
	Refresh(context.Context, ...ImagesProviderID) error
	GetAuth(context.Context, ImagesProviderID, *auth.ResolutionOverrides) (*auth.AuthResult, error)
	GetModelAuth(context.Context, *ImagesModel, *auth.ResolutionOverrides) (*auth.AuthResult, error)
	GenerateImages(context.Context, ImagesRequest) (*AssistantImages, error)
}

type MutableImagesModels interface {
	ImagesModels
	SetProvider(ImagesProvider)
	DeleteProvider(ImagesProviderID)
	ClearProviders()
}

type ImagesModelsOptions struct {
	Credentials auth.CredentialStore
	AuthContext auth.AuthContext
}

type imagesModels struct {
	mu          sync.RWMutex
	providers   []ImagesProvider
	credentials auth.CredentialStore
	authContext auth.AuthContext
}

func CreateImagesModels(options ...ImagesModelsOptions) MutableImagesModels {
	if len(options) > 1 {
		panic("ai: CreateImagesModels accepts at most one options value")
	}
	var option ImagesModelsOptions
	if len(options) == 1 {
		option = options[0]
	}
	if option.Credentials == nil {
		option.Credentials = auth.NewMemoryStore(nil)
	}
	if option.AuthContext == nil {
		option.AuthContext = auth.EnvironmentContext{}
	}
	return &imagesModels{credentials: option.Credentials, authContext: option.AuthContext}
}

func (models *imagesModels) SetProvider(provider ImagesProvider) {
	models.mu.Lock()
	defer models.mu.Unlock()
	for index, current := range models.providers {
		if current.ID() == provider.ID() {
			models.providers[index] = provider
			return
		}
	}
	models.providers = append(models.providers, provider)
}

func (models *imagesModels) DeleteProvider(id ImagesProviderID) {
	models.mu.Lock()
	defer models.mu.Unlock()
	for index, provider := range models.providers {
		if provider.ID() == id {
			models.providers = append(models.providers[:index], models.providers[index+1:]...)
			return
		}
	}
}

func (models *imagesModels) ClearProviders() {
	models.mu.Lock()
	models.providers = nil
	models.mu.Unlock()
}

func (models *imagesModels) GetProviders() []ImagesProvider {
	models.mu.RLock()
	defer models.mu.RUnlock()
	return append([]ImagesProvider(nil), models.providers...)
}

func (models *imagesModels) GetProvider(id ImagesProviderID) ImagesProvider {
	for _, provider := range models.GetProviders() {
		if provider.ID() == id {
			return provider
		}
	}
	return nil
}

func (models *imagesModels) GetModels(providerID ...ImagesProviderID) []ImagesModel {
	if len(providerID) > 1 {
		panic("ai: GetModels accepts at most one provider ID")
	}
	providers := models.GetProviders()
	if len(providerID) == 1 {
		provider := models.GetProvider(providerID[0])
		if provider == nil {
			return []ImagesModel{}
		}
		providers = []ImagesProvider{provider}
	}
	result := make([]ImagesModel, 0)
	for _, provider := range providers {
		listed, err := provider.GetModels()
		if err == nil {
			result = append(result, listed...)
		}
	}
	return result
}

func (models *imagesModels) GetModel(provider ImagesProviderID, id string) *ImagesModel {
	listed := models.GetModels(provider)
	for index := range listed {
		if listed[index].ID == id {
			return &listed[index]
		}
	}
	return nil
}

func (models *imagesModels) Refresh(ctx context.Context, providerID ...ImagesProviderID) error {
	if len(providerID) > 1 {
		panic("ai: Refresh accepts at most one provider ID")
	}
	if len(providerID) == 1 {
		provider := models.GetProvider(providerID[0])
		refresh, ok := provider.(refreshableImagesProvider)
		if !ok {
			return nil
		}
		if err := refresh.RefreshModels(ctx); err != nil {
			var modelError *ModelsError
			if errors.As(err, &modelError) {
				return modelError
			}
			return &ModelsError{Code: ModelsErrorModelSource, Message: fmt.Sprintf("Model refresh failed for %s", providerID[0]), Cause: err}
		}
		return nil
	}

	var wait sync.WaitGroup
	for _, provider := range models.GetProviders() {
		refresh, ok := provider.(refreshableImagesProvider)
		if !ok {
			continue
		}
		wait.Add(1)
		go func() {
			defer wait.Done()
			_ = refresh.RefreshModels(ctx)
		}()
	}
	wait.Wait()
	return nil
}

func (models *imagesModels) GetAuth(ctx context.Context, providerID ImagesProviderID, overrides *auth.ResolutionOverrides) (*auth.AuthResult, error) {
	provider := models.GetProvider(providerID)
	if provider == nil {
		return nil, nil
	}
	return auth.ResolveProviderAuth(ctx, string(providerID), provider.Auth(), models.credentials, models.authContext, overrides)
}

func (models *imagesModels) GetModelAuth(ctx context.Context, model *ImagesModel, overrides *auth.ResolutionOverrides) (*auth.AuthResult, error) {
	if model == nil {
		return nil, nil
	}
	return models.GetAuth(ctx, model.Provider, overrides)
}

func (models *imagesModels) GenerateImages(ctx context.Context, request ImagesRequest) (*AssistantImages, error) {
	if request.Model == nil {
		return nil, errors.New("ai: images model is nil")
	}
	provider := models.GetProvider(request.Model.Provider)
	if provider == nil {
		return imagesErrorResult(request.Model, fmt.Errorf("Unknown provider: %s", request.Model.Provider)), nil //nolint:staticcheck // Upstream text.
	}

	var overrides *auth.ResolutionOverrides
	if request.Options != nil {
		overrides = &auth.ResolutionOverrides{APIKey: request.Options.APIKey, Env: request.Options.Env}
	}
	resolved, err := models.GetModelAuth(ctx, request.Model, overrides)
	if err != nil {
		return imagesErrorResult(request.Model, err), nil
	}
	if resolved != nil {
		model := *request.Model
		if resolved.Auth.BaseURL != nil && *resolved.Auth.BaseURL != "" {
			model.BaseURL = *resolved.Auth.BaseURL
		}
		options := ImagesOptions{}
		if request.Options != nil {
			options = *request.Options
		}
		if options.APIKey == nil {
			options.APIKey = resolved.Auth.APIKey
		}
		options.Headers = mergeImagesHeaders(resolved.Auth.Headers, options.Headers)
		options.Env = mergeImagesEnv(resolved.Env, options.Env)
		request.Model, request.Options = &model, &options
	}
	result, err := provider.GenerateImages(ctx, request)
	if err != nil {
		return imagesErrorResult(request.Model, err), nil
	}
	return result, nil
}

func imagesErrorResult(model *ImagesModel, err error) *AssistantImages {
	message := err.Error()
	return &AssistantImages{
		API: model.API, Provider: model.Provider, Model: model.ID, Output: ImagesContent{},
		StopReason: ImagesStopReasonError, ErrorMessage: &message, Timestamp: time.Now().UnixMilli(),
	}
}

func mergeImagesHeaders(base, override ProviderHeaders) ProviderHeaders {
	if base == nil && override == nil {
		return nil
	}
	result := make(ProviderHeaders, len(base)+len(override))
	for name, value := range base {
		result[name] = value
	}
	for name, value := range override {
		result[name] = value
	}
	return result
}

func mergeImagesEnv(base map[string]string, override ProviderEnv) ProviderEnv {
	if base == nil && override == nil {
		return nil
	}
	result := make(ProviderEnv, len(base)+len(override))
	for name, value := range base {
		result[name] = value
	}
	for name, value := range override {
		result[name] = value
	}
	return result
}

type CreateImagesProviderOptions struct {
	ID            ImagesProviderID
	Name          string
	Auth          auth.ProviderAuth
	Models        []ImagesModel
	RefreshModels func(context.Context) ([]ImagesModel, error)
	API           ImagesFunction
}

type createdImagesProvider struct {
	id     ImagesProviderID
	name   string
	auth   auth.ProviderAuth
	api    ImagesFunction
	mu     sync.RWMutex
	models []ImagesModel
}

func CreateImagesProvider(input CreateImagesProviderOptions) ImagesProvider {
	name := input.Name
	if name == "" {
		name = string(input.ID)
	}
	provider := &createdImagesProvider{
		id: input.ID, name: name, auth: input.Auth, api: input.API,
		models: append([]ImagesModel(nil), input.Models...),
	}
	if input.RefreshModels == nil {
		return provider
	}
	return &dynamicImagesProvider{createdImagesProvider: provider, refresh: input.RefreshModels}
}

func (provider *createdImagesProvider) ID() ImagesProviderID    { return provider.id }
func (provider *createdImagesProvider) Name() string            { return provider.name }
func (provider *createdImagesProvider) Auth() auth.ProviderAuth { return provider.auth }
func (provider *createdImagesProvider) GetModels() ([]ImagesModel, error) {
	provider.mu.RLock()
	defer provider.mu.RUnlock()
	return append([]ImagesModel(nil), provider.models...), nil
}
func (provider *createdImagesProvider) GenerateImages(ctx context.Context, request ImagesRequest) (*AssistantImages, error) {
	return provider.api(ctx, request)
}

type imagesRefresh struct {
	done chan struct{}
	err  error
}

type dynamicImagesProvider struct {
	*createdImagesProvider
	refresh   func(context.Context) ([]ImagesModel, error)
	refreshMu sync.Mutex
	inflight  *imagesRefresh
}

func (provider *dynamicImagesProvider) RefreshModels(ctx context.Context) error {
	provider.refreshMu.Lock()
	if inflight := provider.inflight; inflight != nil {
		provider.refreshMu.Unlock()
		<-inflight.done
		return inflight.err
	}
	inflight := &imagesRefresh{done: make(chan struct{})}
	provider.inflight = inflight
	provider.refreshMu.Unlock()

	listed, err := provider.refresh(ctx)
	if err == nil {
		provider.mu.Lock()
		provider.models = append([]ImagesModel(nil), listed...)
		provider.mu.Unlock()
	}

	provider.refreshMu.Lock()
	inflight.err = err
	provider.inflight = nil
	close(inflight.done)
	provider.refreshMu.Unlock()
	return err
}
