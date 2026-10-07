package web

import "testing"

// TestStaticAssetsCoverEveryName is the guard the panic used to be: a file the
// embed cannot find, or one that is empty, fails here rather than at a request
// nobody made yet. The vendored license is intentionally not in assetNames: it
// ships with the source, not over the route.
func TestStaticAssetsCoverEveryName(t *testing.T) {
	t.Parallel()

	for name := range assetNames {
		asset, ok := staticAssets[name]
		if !ok {
			t.Errorf("asset %s is named but not embedded", name)
			continue
		}
		if len(asset.body) == 0 {
			t.Errorf("asset %s is embedded but empty", name)
		}
		if asset.contentType == "" {
			t.Errorf("asset %s has no content type", name)
		}
	}
	if _, ok := assetNames["htmx-2.0.4.LICENSE.txt"]; ok {
		t.Error("the vendored license is served over the route, want it only in the source")
	}
}
