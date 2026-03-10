package scraper

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strings"
)

// ParsePlacePhotos extracts photo URLs from a Google Maps place detail HTML page.
// The page embeds JSON data in script tags with the same )]}' prefix format.
func ParsePlacePhotos(body []byte) []string {
	var allPhotos []string

	// The place detail page contains multiple JSON blobs in the HTML.
	// Extract all JSON arrays that start with the anti-XSS prefix.
	chunks := extractJSONChunks(body)
	for _, chunk := range chunks {
		photos := extractPhotosFromJSON(chunk)
		allPhotos = append(allPhotos, photos...)
	}

	return dedupStrings(allPhotos)
}

// jsonChunkPattern matches the anti-XSS prefix followed by JSON data in script tags.
var jsonChunkPattern = regexp.MustCompile(`\)\]\}'\n(\[[\s\S]*?\])\s*(?:</script>|$)`)

// extractJSONChunks finds all JSON array blobs in the HTML page.
func extractJSONChunks(body []byte) [][]byte {
	// Method 1: Look for )]}' prefixed chunks (same as tbm=map)
	var chunks [][]byte

	// If the body itself starts with )]}', treat it as a single JSON blob
	if idx := bytes.Index(body, []byte(")]}'\n")); idx >= 0 && idx < 10 {
		chunks = append(chunks, body[idx+5:])
	}

	// Method 2: Find JSON blobs in HTML script tags
	// Google Maps embeds data in window.APP_INITIALIZATION_STATE or similar
	for _, pattern := range []string{
		`AF_initDataCallback\({[^}]*data:`,
		`window\.APP_INITIALIZATION_STATE\s*=\s*`,
	} {
		re := regexp.MustCompile(pattern)
		locs := re.FindAllIndex(body, -1)
		for _, loc := range locs {
			start := loc[1]
			if start < len(body) {
				chunk := extractBalancedJSON(body[start:])
				if chunk != nil {
					chunks = append(chunks, chunk)
				}
			}
		}
	}

	return chunks
}

// extractBalancedJSON extracts a JSON array or value from the beginning of data.
func extractBalancedJSON(data []byte) []byte {
	// Skip whitespace
	i := 0
	for i < len(data) && (data[i] == ' ' || data[i] == '\t' || data[i] == '\n' || data[i] == '\r') {
		i++
	}
	if i >= len(data) {
		return nil
	}

	// Must start with [ or "
	if data[i] != '[' && data[i] != '"' {
		return nil
	}

	// Try to unmarshal progressively larger chunks
	// This is brute-force but safe for the size of data we handle
	for end := i + 1; end <= len(data) && end-i < 5*1024*1024; end++ {
		if data[end-1] == ']' || data[end-1] == '"' {
			var test json.RawMessage
			if json.Unmarshal(data[i:end], &test) == nil {
				return data[i:end]
			}
		}
	}
	return nil
}

// googlePhotoURLPrefix identifies Google-hosted photo URLs.
const googlePhotoURLPrefix = "https://lh"

// extractPhotosFromJSON recursively walks a JSON structure to find Google photo URLs.
func extractPhotosFromJSON(data []byte) []string {
	var raw any
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil
	}

	var photos []string
	walkJSON(raw, func(s string) {
		if isGooglePhotoURL(s) {
			// Normalize to high-res: strip size params and request large version
			s = normalizePhotoURL(s)
			photos = append(photos, s)
		}
	})
	return photos
}

// walkJSON recursively walks any JSON value, calling fn for every string found.
func walkJSON(v any, fn func(string)) {
	switch val := v.(type) {
	case string:
		fn(val)
	case []any:
		for _, item := range val {
			walkJSON(item, fn)
		}
	case map[string]any:
		for _, item := range val {
			walkJSON(item, fn)
		}
	}
}

// isGooglePhotoURL checks if a string looks like a Google-hosted photo URL.
func isGooglePhotoURL(s string) bool {
	if !strings.HasPrefix(s, googlePhotoURLPrefix) {
		return false
	}
	// Must be a googleusercontent.com or ggpht.com photo
	return strings.Contains(s, "googleusercontent.com") || strings.Contains(s, "ggpht.com")
}

// normalizePhotoURL strips size parameters and requests a high-resolution version.
func normalizePhotoURL(u string) string {
	// Google photo URLs often end with =s123-w456-h789 or =wXXX-hYYY etc.
	// Strip the size suffix and request max resolution.
	if idx := strings.LastIndex(u, "="); idx > 0 {
		u = u[:idx]
	}
	return u + "=s1600"
}

// dedupStrings removes duplicate strings while preserving order.
func dedupStrings(ss []string) []string {
	seen := make(map[string]bool, len(ss))
	var out []string
	for _, s := range ss {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}
