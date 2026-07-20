package models

import "github.com/OrdalieTech/pi-go/ai"

func openRouterImages() []ai.ImagesModel {
	return []ai.ImagesModel{
		imageModel("black-forest-labs/flux.2-flex", "Black Forest Labs: FLUX.2 Flex", ai.InputModalities{ai.InputText, ai.InputImage}, ai.InputModalities{ai.InputImage}, ai.ModelCostRates{}),
		imageModel("black-forest-labs/flux.2-klein-4b", "Black Forest Labs: FLUX.2 Klein 4B", ai.InputModalities{ai.InputText, ai.InputImage}, ai.InputModalities{ai.InputImage}, ai.ModelCostRates{}),
		imageModel("black-forest-labs/flux.2-max", "Black Forest Labs: FLUX.2 Max", ai.InputModalities{ai.InputText, ai.InputImage}, ai.InputModalities{ai.InputImage}, ai.ModelCostRates{}),
		imageModel("black-forest-labs/flux.2-pro", "Black Forest Labs: FLUX.2 Pro", ai.InputModalities{ai.InputText, ai.InputImage}, ai.InputModalities{ai.InputImage}, ai.ModelCostRates{}),
		imageModel("bytedance-seed/seedream-4.5", "ByteDance Seed: Seedream 4.5", ai.InputModalities{ai.InputImage, ai.InputText}, ai.InputModalities{ai.InputImage}, ai.ModelCostRates{}),
		imageModel("google/gemini-2.5-flash-image", "Google: Nano Banana (Gemini 2.5 Flash Image)", ai.InputModalities{ai.InputImage, ai.InputText}, ai.InputModalities{ai.InputImage, ai.InputText}, ai.ModelCostRates{Input: 0.3, Output: 2.5, CacheRead: 0.03, CacheWrite: 0.08333333333333334}),
		imageModel("google/gemini-3-pro-image", "Google: Nano Banana Pro (Gemini 3 Pro Image)", ai.InputModalities{ai.InputImage, ai.InputText}, ai.InputModalities{ai.InputImage, ai.InputText}, ai.ModelCostRates{Input: 2, Output: 12, CacheRead: 0.19999999999999998, CacheWrite: 0.375}),
		imageModel("google/gemini-3-pro-image-preview", "Google: Nano Banana Pro (Gemini 3 Pro Image Preview)", ai.InputModalities{ai.InputImage, ai.InputText}, ai.InputModalities{ai.InputImage, ai.InputText}, ai.ModelCostRates{Input: 2, Output: 12, CacheRead: 0.19999999999999998, CacheWrite: 0.375}),
		imageModel("google/gemini-3.1-flash-image", "Google: Nano Banana 2 (Gemini 3.1 Flash Image)", ai.InputModalities{ai.InputImage, ai.InputText}, ai.InputModalities{ai.InputImage, ai.InputText}, ai.ModelCostRates{Input: 0.5, Output: 3}),
		imageModel("google/gemini-3.1-flash-image-preview", "Google: Nano Banana 2 (Gemini 3.1 Flash Image Preview)", ai.InputModalities{ai.InputImage, ai.InputText}, ai.InputModalities{ai.InputImage, ai.InputText}, ai.ModelCostRates{Input: 0.5, Output: 3}),
		imageModel("google/gemini-3.1-flash-lite-image", "Google: Nano Banana 2 Lite (Gemini 3.1 Flash Lite Image)", ai.InputModalities{ai.InputImage, ai.InputText}, ai.InputModalities{ai.InputImage, ai.InputText}, ai.ModelCostRates{Input: 0.25, Output: 1.5}),
		imageModel("microsoft/mai-image-2.5", "Microsoft: MAI-Image-2.5", ai.InputModalities{ai.InputText, ai.InputImage}, ai.InputModalities{ai.InputImage}, ai.ModelCostRates{Input: 5}),
		imageModel("openai/gpt-5-image", "OpenAI: GPT-5 Image", ai.InputModalities{ai.InputImage, ai.InputText}, ai.InputModalities{ai.InputImage, ai.InputText}, ai.ModelCostRates{Input: 10, Output: 10, CacheRead: 1.25}),
		imageModel("openai/gpt-5-image-mini", "OpenAI: GPT-5 Image Mini", ai.InputModalities{ai.InputImage, ai.InputText}, ai.InputModalities{ai.InputImage, ai.InputText}, ai.ModelCostRates{Input: 2.5, Output: 2, CacheRead: 0.25}),
		imageModel("openai/gpt-5.4-image-2", "OpenAI: GPT-5.4 Image 2", ai.InputModalities{ai.InputImage, ai.InputText}, ai.InputModalities{ai.InputImage, ai.InputText}, ai.ModelCostRates{Input: 8, Output: 15, CacheRead: 2}),
		imageModel("openai/gpt-image-1", "OpenAI: GPT Image 1", ai.InputModalities{ai.InputText, ai.InputImage}, ai.InputModalities{ai.InputImage}, ai.ModelCostRates{Input: 10, Output: 10, CacheRead: 1.25}),
		imageModel("openai/gpt-image-1-mini", "OpenAI: GPT Image 1 Mini", ai.InputModalities{ai.InputText, ai.InputImage}, ai.InputModalities{ai.InputImage}, ai.ModelCostRates{Input: 2.5, Output: 2.5, CacheRead: 0.25}),
		imageModel("openai/gpt-image-2", "OpenAI: GPT Image 2", ai.InputModalities{ai.InputText, ai.InputImage}, ai.InputModalities{ai.InputImage}, ai.ModelCostRates{Input: 8, Output: 8, CacheRead: 2}),
		imageModel("openrouter/auto", "Auto Router", ai.InputModalities{ai.InputText, ai.InputImage}, ai.InputModalities{ai.InputText, ai.InputImage}, ai.ModelCostRates{Input: -1000000, Output: -1000000}),
		imageModel("recraft/recraft-v3", "Recraft: Recraft V3", ai.InputModalities{ai.InputText, ai.InputImage}, ai.InputModalities{ai.InputImage}, ai.ModelCostRates{}),
		imageModel("recraft/recraft-v4", "Recraft: Recraft V4", ai.InputModalities{ai.InputText, ai.InputImage}, ai.InputModalities{ai.InputImage}, ai.ModelCostRates{}),
		imageModel("recraft/recraft-v4-pro", "Recraft: Recraft V4 Pro", ai.InputModalities{ai.InputText, ai.InputImage}, ai.InputModalities{ai.InputImage}, ai.ModelCostRates{}),
		imageModel("recraft/recraft-v4-pro-vector", "Recraft: Recraft V4 Pro Vector", ai.InputModalities{ai.InputText, ai.InputImage}, ai.InputModalities{ai.InputImage}, ai.ModelCostRates{}),
		imageModel("recraft/recraft-v4-vector", "Recraft: Recraft V4 Vector", ai.InputModalities{ai.InputText, ai.InputImage}, ai.InputModalities{ai.InputImage}, ai.ModelCostRates{}),
		imageModel("recraft/recraft-v4.1", "Recraft: Recraft V4.1", ai.InputModalities{ai.InputText, ai.InputImage}, ai.InputModalities{ai.InputImage}, ai.ModelCostRates{}),
		imageModel("recraft/recraft-v4.1-pro", "Recraft: Recraft V4.1 Pro", ai.InputModalities{ai.InputText, ai.InputImage}, ai.InputModalities{ai.InputImage}, ai.ModelCostRates{}),
		imageModel("recraft/recraft-v4.1-pro-vector", "Recraft: Recraft V4.1 Pro Vector", ai.InputModalities{ai.InputText, ai.InputImage}, ai.InputModalities{ai.InputImage}, ai.ModelCostRates{}),
		imageModel("recraft/recraft-v4.1-utility", "Recraft: Recraft V4.1 Utility", ai.InputModalities{ai.InputText, ai.InputImage}, ai.InputModalities{ai.InputImage}, ai.ModelCostRates{}),
		imageModel("recraft/recraft-v4.1-utility-pro", "Recraft: Recraft V4.1 Utility Pro", ai.InputModalities{ai.InputText, ai.InputImage}, ai.InputModalities{ai.InputImage}, ai.ModelCostRates{}),
		imageModel("recraft/recraft-v4.1-vector", "Recraft: Recraft V4.1 Vector", ai.InputModalities{ai.InputText, ai.InputImage}, ai.InputModalities{ai.InputImage}, ai.ModelCostRates{}),
		imageModel("sourceful/riverflow-v2-fast", "Sourceful: Riverflow V2 Fast", ai.InputModalities{ai.InputText, ai.InputImage}, ai.InputModalities{ai.InputImage}, ai.ModelCostRates{}),
		imageModel("sourceful/riverflow-v2-pro", "Sourceful: Riverflow V2 Pro", ai.InputModalities{ai.InputText, ai.InputImage}, ai.InputModalities{ai.InputImage}, ai.ModelCostRates{}),
		imageModel("sourceful/riverflow-v2.5-fast", "Sourceful: Riverflow V2.5 Fast", ai.InputModalities{ai.InputText, ai.InputImage}, ai.InputModalities{ai.InputImage}, ai.ModelCostRates{}),
		imageModel("sourceful/riverflow-v2.5-pro", "Sourceful: Riverflow V2.5 Pro", ai.InputModalities{ai.InputText, ai.InputImage}, ai.InputModalities{ai.InputImage}, ai.ModelCostRates{}),
		imageModel("x-ai/grok-imagine-image-quality", "xAI: Grok Imagine Image Quality", ai.InputModalities{ai.InputText, ai.InputImage}, ai.InputModalities{ai.InputImage}, ai.ModelCostRates{}),
	}
}

func BuiltinImages(provider ai.ImagesProviderID) []ai.ImagesModel {
	if provider != ai.ImagesProviderOpenRouter {
		return []ai.ImagesModel{}
	}
	return openRouterImages()
}

func imageModel(id, name string, input, output ai.InputModalities, cost ai.ModelCostRates) ai.ImagesModel {
	return ai.ImagesModel{
		ID: id, Name: name, API: ai.ImagesAPIOpenRouter, Provider: ai.ImagesProviderOpenRouter,
		BaseURL: "https://openrouter.ai/api/v1", Input: input, Output: output,
		Cost: ai.ModelCost{ModelCostRates: cost},
	}
}
