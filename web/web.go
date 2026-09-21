package web

import (
	"bytes"
	_ "embed"
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
