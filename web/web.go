package web

import (
	"bytes"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"sync"
)

//go:embed index.html
var IndexHTML []byte

// bootPlaceholder is the literal JSON the embedded document ships with. It is
// swapped for the live configuration when the dashboard is served. Keep it in
// sync with the <script id="cpa-usage-boot"> element in index.html.
var bootPlaceholder = []byte(`{"unauthenticated_api":false}`)

// BootConfig is handed to the dashboard so it knows which API base to use and
// whether it has to ask for a management key.
type BootConfig struct {
	// UnauthenticatedAPI reports whether the JSON endpoints are also mounted on
	// the host's unauthenticated resource path.
	UnauthenticatedAPI bool `json:"unauthenticated_api"`
}

var (
	bootCacheMu sync.RWMutex
	bootCache   = map[BootConfig][]byte{}

	etagCacheMu sync.RWMutex
	etagCache   = map[BootConfig]string{}
)

// Dashboard returns the dashboard document with cfg injected.
//
// json.Marshal escapes '<', so the encoded value cannot terminate the script
// element it is embedded in.
func Dashboard(cfg BootConfig) []byte {
	bootCacheMu.RLock()
	cached, ok := bootCache[cfg]
	bootCacheMu.RUnlock()
	if ok {
		return cached
	}

	encoded, err := json.Marshal(cfg)
	if err != nil {
		return IndexHTML
	}
	doc := bytes.Replace(IndexHTML, bootPlaceholder, encoded, 1)

	bootCacheMu.Lock()
	bootCache[cfg] = doc
	bootCacheMu.Unlock()
	return doc
}

// DashboardETag returns a strong validator for the rendered dashboard so the
// caller can answer If-None-Match with 304 and skip transferring (and the
// browser re-parsing) the ~250KB document on every refresh.
func DashboardETag(cfg BootConfig) string {
	etagCacheMu.RLock()
	cached, ok := etagCache[cfg]
	etagCacheMu.RUnlock()
	if ok {
		return cached
	}

	sum := sha256.Sum256(Dashboard(cfg))
	etag := `"` + hex.EncodeToString(sum[:16]) + `"`

	etagCacheMu.Lock()
	etagCache[cfg] = etag
	etagCacheMu.Unlock()
	return etag
}
