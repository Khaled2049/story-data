package httpapi

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
)

const (
	publicStoryListCacheControl   = "public, max-age=0, s-maxage=30, stale-while-revalidate=300"
	publicStoryDetailCacheControl = "public, max-age=0, s-maxage=30"
	publicChapterCacheControl     = "public, max-age=0, s-maxage=60, stale-while-revalidate=600"
)

func respondCacheable(w http.ResponseWriter, r *http.Request, v any, e error, cacheControl string) {
	if e != nil {
		respond(w, nil, e)
		return
	}
	body, err := json.Marshal(v)
	if err != nil {
		respond(w, nil, err)
		return
	}
	body = append(body, '\n')
	sum := sha256.Sum256(body)
	etag := `W/"` + hex.EncodeToString(sum[:16]) + `"`
	h := w.Header()
	h.Set("Cache-Control", cacheControl)
	h.Set("ETag", etag)
	if etagMatches(r.Header.Get("If-None-Match"), etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

func etagMatches(ifNoneMatch, etag string) bool {
	want := strings.TrimPrefix(etag, "W/")
	for candidate := range strings.SplitSeq(ifNoneMatch, ",") {
		candidate = strings.TrimSpace(candidate)
		if candidate == "*" || strings.TrimPrefix(candidate, "W/") == want {
			return true
		}
	}
	return false
}
