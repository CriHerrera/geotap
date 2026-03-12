package scraper

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strings"
)

const maxPhotosPerPlace = 30

var photoURLPattern = regexp.MustCompile(`https://lh[0-9]+\.googleusercontent\.com/[^"\\]+`)

// ParsePlacePhotos extracts photo URLs from a maps/preview/place response.
// Returns a JSON-encoded string array of unique photo URLs, or "" if none found.
func ParsePlacePhotos(body []byte) string {
	// Strip anti-XSS prefix )]}'\n
	if idx := bytes.IndexByte(body, '\n'); idx >= 0 && idx < 10 {
		body = body[idx+1:]
	}

	var raw []any
	if err := json.Unmarshal(body, &raw); err != nil {
		return ""
	}

	// Place data is at response[6]
	place := safeSlice(safeGet(raw, 6))
	if len(place) == 0 {
		return ""
	}

	// Extract photo URLs from known locations:
	// - place[72]: cover photo
	// - place[122][1][N][5][M][0]: gallery photos from business posts
	// Fall back to regex scan of entire place data for robustness.
	matches := photoURLPattern.FindAllString(string(body), -1)
	if len(matches) == 0 {
		return ""
	}

	// Deduplicate by base URL (strip size params like =w400-h300, =s44, =h400)
	seen := make(map[string]bool)
	var unique []string
	for _, u := range matches {
		// Unescape unicode sequences from JSON (e.g. \u003d -> =)
		u = strings.ReplaceAll(u, `\u003d`, "=")

		base := u
		if idx := strings.Index(base, "="); idx > 0 {
			base = base[:idx]
		}
		if seen[base] {
			continue
		}
		seen[base] = true

		// Normalize to high-res version
		if idx := strings.Index(u, "="); idx > 0 {
			u = u[:idx] + "=w1200"
		}
		unique = append(unique, u)

		if len(unique) >= maxPhotosPerPlace {
			break
		}
	}

	if len(unique) == 0 {
		return ""
	}

	data, err := json.Marshal(unique)
	if err != nil {
		return ""
	}
	return string(data)
}
