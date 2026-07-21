package ai_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/OrdalieTech/pigo/ai"
	"github.com/OrdalieTech/pigo/ai/auth"
)

type imagesAuthContext map[string]string

func (environment imagesAuthContext) Env(_ context.Context, name string) (string, bool) {
	value, ok := environment[name]
	return value, ok
}

func (imagesAuthContext) FileExists(context.Context, string) bool { return false }

type imagesAPIKeyAuth func(context.Context, auth.AuthContext, *auth.Credential) (*auth.AuthResult, error)

func (imagesAPIKeyAuth) Name() string { return "Test key" }

func (resolve imagesAPIKeyAuth) Resolve(
	ctx context.Context,
	authContext auth.AuthContext,
	credential *auth.Credential,
) (*auth.AuthResult, error) {
	return resolve(ctx, authContext, credential)
}

type brokenImagesProvider struct{ id ai.ImagesProviderID }

func (provider brokenImagesProvider) ID() ai.ImagesProviderID     { return provider.id }
func (brokenImagesProvider) Name() string                         { return "broken" }
func (brokenImagesProvider) Auth() auth.ProviderAuth              { return auth.ProviderAuth{} }
func (brokenImagesProvider) GetModels() ([]ai.ImagesModel, error) { return nil, errors.New("broken") }
func (brokenImagesProvider) GenerateImages(context.Context, ai.ImagesRequest) (*ai.AssistantImages, error) {
	return nil, errors.New("broken")
}

func imageModel(provider ai.ImagesProviderID, id string) ai.ImagesModel {
	return ai.ImagesModel{
		ID: id, Name: id, API: "test-images", Provider: provider,
		BaseURL: "https://example.test/v1", Input: ai.InputModalities{ai.InputText},
		Output: ai.InputModalities{ai.InputImage}, Cost: ai.ModelCost{},
	}
}

func imageResult(model *ai.ImagesModel) *ai.AssistantImages {
	return &ai.AssistantImages{
		API: model.API, Provider: model.Provider, Model: model.ID,
		Output:     ai.ImagesContent{&ai.ImageContent{Data: "aGk=", MimeType: "image/png"}},
		StopReason: ai.ImagesStopReasonStop, Timestamp: time.Now().UnixMilli(),
	}
}

func staticImagesProvider(id ai.ImagesProviderID, models ...ai.ImagesModel) ai.ImagesProvider {
	return ai.CreateImagesProvider(ai.CreateImagesProviderOptions{
		ID: id, Auth: auth.ProviderAuth{APIKey: imagesAPIKeyAuth(func(context.Context, auth.AuthContext, *auth.Credential) (*auth.AuthResult, error) {
			return &auth.AuthResult{Auth: auth.ModelAuth{}}, nil
		})},
		Models: models,
		API: func(_ context.Context, request ai.ImagesRequest) (*ai.AssistantImages, error) {
			return imageResult(request.Model), nil
		},
	})
}

func TestImagesModelsOrderUpsertAndBestEffort(t *testing.T) {
	models := ai.CreateImagesModels()
	models.SetProvider(staticImagesProvider("p1", imageModel("p1", "m1"), imageModel("p1", "m2")))
	models.SetProvider(staticImagesProvider("p2", imageModel("p2", "m3")))
	models.SetProvider(staticImagesProvider("p1", imageModel("p1", "m4")))
	models.SetProvider(brokenImagesProvider{id: "bad"})

	providers := models.GetProviders()
	if len(providers) != 3 || providers[0].ID() != "p1" || providers[1].ID() != "p2" || providers[2].ID() != "bad" {
		t.Fatalf("provider order after upsert = %#v", providers)
	}
	listed := models.GetModels()
	if len(listed) != 2 || listed[0].ID != "m4" || listed[1].ID != "m3" {
		t.Fatalf("best-effort models = %#v", listed)
	}
	if got := models.GetModels("bad"); len(got) != 0 {
		t.Fatalf("broken provider models = %#v", got)
	}
	if got := models.GetModel("p2", "m3"); got == nil || got.ID != "m3" {
		t.Fatalf("GetModel = %#v", got)
	}
	if got := models.GetModel("p2", "missing"); got != nil {
		t.Fatalf("missing model = %#v", got)
	}

	models.DeleteProvider("p1")
	if models.GetProvider("p1") != nil {
		t.Fatal("deleted provider remains registered")
	}
	models.ClearProviders()
	if got := models.GetProviders(); len(got) != 0 {
		t.Fatalf("providers after clear = %#v", got)
	}
}

func TestImagesModelsConcurrentMutationAndRead(t *testing.T) {
	models := ai.CreateImagesModels()
	p1 := staticImagesProvider("p1", imageModel("p1", "m1"))
	p2 := staticImagesProvider("p2", imageModel("p2", "m2"))
	models.SetProvider(p1)

	start := make(chan struct{})
	var wait sync.WaitGroup
	for range 4 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			for range 500 {
				_ = models.GetProviders()
				_ = models.GetProvider("p1")
				_ = models.GetModels()
			}
		}()
	}
	for writer := range 2 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			for iteration := range 500 {
				models.SetProvider(p1)
				models.SetProvider(p2)
				models.DeleteProvider("p2")
				if (writer+iteration)%10 == 0 {
					models.ClearProviders()
				}
			}
		}()
	}
	close(start)
	wait.Wait()
}

func TestImagesModelsAuthMergeAndErrorResults(t *testing.T) {
	ctx := context.Background()
	providerKey, requestKey := "provider-key", "request-key"
	providerHeader, requestHeader := "provider", "request"
	providerURL := "https://auth.example/v1"
	var calls []ai.ImagesRequest
	provider := ai.CreateImagesProvider(ai.CreateImagesProviderOptions{
		ID: "p1",
		Auth: auth.ProviderAuth{APIKey: imagesAPIKeyAuth(func(_ context.Context, _ auth.AuthContext, credential *auth.Credential) (*auth.AuthResult, error) {
			key := providerKey
			if credential != nil && credential.Key != nil {
				key = *credential.Key
			}
			return &auth.AuthResult{
				Auth: auth.ModelAuth{APIKey: &key, BaseURL: &providerURL, Headers: map[string]*string{
					"Provider": &providerHeader, "Shared": &providerHeader,
				}},
				Env: map[string]string{"PROVIDER_ONLY": "provider", "SHARED": "provider"},
			}, nil
		})},
		Models: []ai.ImagesModel{imageModel("p1", "model-a")},
		API: func(_ context.Context, request ai.ImagesRequest) (*ai.AssistantImages, error) {
			calls = append(calls, request)
			return imageResult(request.Model), nil
		},
	})
	models := ai.CreateImagesModels(ai.ImagesModelsOptions{AuthContext: imagesAuthContext{}})
	models.SetProvider(provider)
	model := models.GetModel("p1", "model-a")

	resolved, err := models.GetModelAuth(ctx, model, &auth.ResolutionOverrides{APIKey: &requestKey})
	if err != nil || resolved == nil || resolved.Auth.APIKey == nil || *resolved.Auth.APIKey != requestKey {
		t.Fatalf("explicit auth = %#v, %v", resolved, err)
	}
	if _, err := models.GenerateImages(ctx, ai.ImagesRequest{Model: model, Context: ai.ImagesContext{}, Options: &ai.ImagesOptions{
		APIKey:  &requestKey,
		Headers: ai.ProviderHeaders{"Request": &requestHeader, "Shared": &requestHeader},
		Env:     ai.ProviderEnv{"REQUEST_ONLY": "request", "SHARED": "request"},
	}}); err != nil {
		t.Fatal(err)
	}
	call := calls[0]
	if call.Model.BaseURL != providerURL || model.BaseURL == providerURL {
		t.Fatalf("request/original base URLs = %q / %q", call.Model.BaseURL, model.BaseURL)
	}
	if call.Options.APIKey == nil || *call.Options.APIKey != requestKey || *call.Options.Headers["Provider"] != providerHeader ||
		*call.Options.Headers["Request"] != requestHeader || *call.Options.Headers["Shared"] != requestHeader {
		t.Fatalf("merged auth options = %#v", call.Options)
	}
	if call.Options.Env["PROVIDER_ONLY"] != "provider" || call.Options.Env["REQUEST_ONLY"] != "request" ||
		call.Options.Env["SHARED"] != "request" {
		t.Fatalf("merged auth env = %#v", call.Options.Env)
	}

	ghost := imageModel("ghost", "m")
	result, err := models.GenerateImages(ctx, ai.ImagesRequest{Model: &ghost, Context: ai.ImagesContext{}})
	if err != nil || result.StopReason != ai.ImagesStopReasonError || result.ErrorMessage == nil ||
		*result.ErrorMessage != "Unknown provider: ghost" || len(result.Output) != 0 {
		t.Fatalf("unknown-provider result = %#v, %v", result, err)
	}

	models.SetProvider(ai.CreateImagesProvider(ai.CreateImagesProviderOptions{
		ID: "reject", Auth: auth.ProviderAuth{APIKey: auth.EnvAPIKeyAuth{DisplayName: "missing", EnvVars: []string{"MISSING"}}},
		Models: []ai.ImagesModel{imageModel("reject", "m")},
		API: func(context.Context, ai.ImagesRequest) (*ai.AssistantImages, error) {
			return nil, errors.New("generate failed")
		},
	}))
	result, err = models.GenerateImages(ctx, ai.ImagesRequest{Model: models.GetModel("reject", "m")})
	if err != nil || result.StopReason != ai.ImagesStopReasonError || result.ErrorMessage == nil || *result.ErrorMessage != "generate failed" {
		t.Fatalf("rejected generation = %#v, %v", result, err)
	}
}

func TestImagesModelsRefreshDeduplicatesRetriesAndAllIsBestEffort(t *testing.T) {
	ctx := context.Background()
	var fetches atomic.Int32
	started, release := make(chan struct{}), make(chan struct{})
	dynamic := ai.CreateImagesProvider(ai.CreateImagesProviderOptions{
		ID: "dynamic", Auth: auth.ProviderAuth{}, Models: nil,
		RefreshModels: func(context.Context) ([]ai.ImagesModel, error) {
			if fetches.Add(1) == 1 {
				close(started)
				<-release
			}
			return []ai.ImagesModel{imageModel("dynamic", "listed")}, nil
		},
		API: func(_ context.Context, request ai.ImagesRequest) (*ai.AssistantImages, error) {
			return imageResult(request.Model), nil
		},
	})
	models := ai.CreateImagesModels()
	models.SetProvider(dynamic)
	errs := make(chan error, 2)
	go func() { errs <- models.Refresh(ctx, "dynamic") }()
	<-started
	go func() { errs <- models.Refresh(ctx, "dynamic") }()
	time.Sleep(50 * time.Millisecond)
	close(release)
	for range 2 {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	if fetches.Load() != 1 || models.GetModel("dynamic", "listed") == nil {
		t.Fatalf("fetches/models = %d / %#v", fetches.Load(), models.GetModels("dynamic"))
	}

	var attempts atomic.Int32
	models.SetProvider(ai.CreateImagesProvider(ai.CreateImagesProviderOptions{
		ID: "flaky", Auth: auth.ProviderAuth{}, Models: []ai.ImagesModel{imageModel("flaky", "old")},
		RefreshModels: func(context.Context) ([]ai.ImagesModel, error) {
			if attempts.Add(1) == 1 {
				return nil, errors.New("fetch failed")
			}
			return []ai.ImagesModel{imageModel("flaky", "new")}, nil
		},
		API: func(_ context.Context, request ai.ImagesRequest) (*ai.AssistantImages, error) {
			return imageResult(request.Model), nil
		},
	}))
	err := models.Refresh(ctx, "flaky")
	var modelError *ai.ModelsError
	if !errors.As(err, &modelError) || modelError.Code != ai.ModelsErrorModelSource || models.GetModel("flaky", "old") == nil {
		t.Fatalf("failed refresh = %v, models %#v", err, models.GetModels("flaky"))
	}
	if err := models.Refresh(ctx, "flaky"); err != nil || models.GetModel("flaky", "new") == nil || attempts.Load() != 2 {
		t.Fatalf("retry = %v, attempts %d, models %#v", err, attempts.Load(), models.GetModels("flaky"))
	}

	models.SetProvider(ai.CreateImagesProvider(ai.CreateImagesProviderOptions{
		ID: "always-fails", Auth: auth.ProviderAuth{},
		RefreshModels: func(context.Context) ([]ai.ImagesModel, error) { return nil, errors.New("still failing") },
		API: func(_ context.Context, request ai.ImagesRequest) (*ai.AssistantImages, error) {
			return imageResult(request.Model), nil
		},
	}))
	if err := models.Refresh(ctx); err != nil {
		t.Fatalf("refresh all must be best-effort: %v", err)
	}
	if err := models.Refresh(ctx, "unknown"); err != nil {
		t.Fatalf("unknown provider refresh = %v", err)
	}
}
