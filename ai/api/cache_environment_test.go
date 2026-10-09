package api

import (
	"testing"

	"github.com/OrdalieTech/orb/ai"
)

func TestCacheRetentionUsesOrbEnvironmentOnly(t *testing.T) {
	t.Setenv("PI_CACHE_RETENTION", "long")
	t.Setenv("ORB_CACHE_RETENTION", "")
	options := &ai.StreamOptions{Env: ai.ProviderEnv{"PI_CACHE_RETENTION": "long"}}
	if got := resolveCacheRetention(options); got != ai.CacheRetentionShort {
		t.Fatalf("Pi environment changed Orb cache retention: %q", got)
	}
	if got := piMessagesCacheRetention(&PiMessagesOptions{StreamOptions: *options}); got != nil {
		t.Fatalf("Pi environment changed pi-messages retention: %v", *got)
	}
	options.Env["ORB_CACHE_RETENTION"] = "long"
	if got := resolveCacheRetention(options); got != ai.CacheRetentionLong {
		t.Fatalf("explicit Orb environment retention = %q", got)
	}
	if got := piMessagesCacheRetention(&PiMessagesOptions{StreamOptions: *options}); got == nil || *got != ai.CacheRetentionLong {
		t.Fatalf("explicit Orb environment pi-messages retention = %v", got)
	}
	t.Setenv("ORB_CACHE_RETENTION", "long")
	if got := resolveCacheRetention(nil); got != ai.CacheRetentionLong {
		t.Fatalf("ambient Orb environment retention = %q", got)
	}
	retention := ai.CacheRetentionNone
	options.CacheRetention = &retention
	if got := resolveCacheRetention(options); got != retention {
		t.Fatalf("explicit cache option lost precedence: %q", got)
	}
}
