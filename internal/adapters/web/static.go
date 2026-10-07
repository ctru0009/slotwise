package web

import (
	"embed"
	"log/slog"
	"net/http"
)

//go:embed static/*
var staticFS embed.FS

// assetNames is the whole public surface of the asset route: the vendored files
// and the content type each is served as. A request names an entry here, so an
// unknown name is a 404 and a future file in static/ does not become reachable
// just by existing.
var assetNames = map[string]string{
	"htmx-2.0.4.min.js": "text/javascript; charset=utf-8",
}

// staticAssets holds the embedded files, read once at startup. A name the
// embed cannot find stays out of the map: the route then answers 404, the
// request never reaches the filesystem, and TestStaticAssetsCoverEveryName
// fails instead of the server quietly serving something empty.
var staticAssets = loadStaticAssets()

type vendoredAsset struct {
	contentType string
	body        []byte
}

// loadStaticAssets reads every named file out of the embedded filesystem.
func loadStaticAssets() map[string]vendoredAsset {
	assets := make(map[string]vendoredAsset, len(assetNames))
	for name, contentType := range assetNames {
		body, err := staticFS.ReadFile("static/" + name)
		if err != nil {
			continue
		}
		assets[name] = vendoredAsset{contentType: contentType, body: body}
	}
	return assets
}

// staticAsset serves one vendored file. The file name carries the version, so
// the response can be cached indefinitely: a new version arrives as a new URL
// rather than as a change to this one.
func (s *server) staticAsset(w http.ResponseWriter, r *http.Request) {
	asset, ok := staticAssets[r.PathValue("file")]
	if !ok {
		fail(w, r, http.StatusNotFound, messageNoRecord)
		return
	}
	w.Header().Set("Content-Type", asset.contentType)
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	if _, err := w.Write(asset.body); err != nil {
		slog.ErrorContext(r.Context(), "writing static asset", "err", err)
	}
}
