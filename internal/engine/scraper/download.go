package scraper

import (
	"fmt"
	"io"
	"log"
	"math/rand/v2"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// DownloadOptions configures photo downloads for a business.
type DownloadOptions struct {
	OutputDir  string        // Base directory for photos (e.g., "output/fotos")
	MaxPhotos  int           // Max photos per business (default 5)
	Resolution string        // Size suffix like "s1200-k-no" or "s0" (default "s1200-k-no")
	Delay      time.Duration // Delay between downloads (default 1.5s)
	Logger     *log.Logger   // Optional logger
}

// DownloadResult holds the outcome of a photo download batch.
type DownloadResult struct {
	Downloaded int
	Errors     int
	Dir        string // Directory where photos were saved
}

// DownloadPlacePhotos downloads photos for a business given its photo URLs.
// Photos are saved to outputDir/{sanitized_name}/foto_1.jpg, foto_2.jpg, etc.
func DownloadPlacePhotos(client *http.Client, name string, photoURLs []string, opts DownloadOptions) DownloadResult {
	if opts.MaxPhotos <= 0 {
		opts.MaxPhotos = 5
	}
	if opts.Resolution == "" {
		opts.Resolution = "s1200-k-no"
	}
	if opts.Delay == 0 {
		opts.Delay = 1500 * time.Millisecond
	}

	result := DownloadResult{}

	// Limit photos
	urls := photoURLs
	if len(urls) > opts.MaxPhotos {
		urls = urls[:opts.MaxPhotos]
	}

	if len(urls) == 0 {
		return result
	}

	// Create business directory
	dirName := sanitizeDirName(name)
	if dirName == "" {
		dirName = "unknown"
	}
	dir := filepath.Join(opts.OutputDir, dirName)
	if err := os.MkdirAll(dir, 0755); err != nil {
		if opts.Logger != nil {
			opts.Logger.Printf("DOWNLOAD_ERROR mkdir=%q err=%v", dir, err)
		}
		return result
	}
	result.Dir = dir

	for i, rawURL := range urls {
		// Replace resolution suffix (skip for Street View URLs which have their own params)
		var photoURL string
		if strings.Contains(rawURL, "streetviewpixels-pa.googleapis.com") {
			photoURL = rawURL
		} else {
			photoURL = setResolution(rawURL, opts.Resolution)
		}

		filename := fmt.Sprintf("foto_%d.jpg", i+1)
		destPath := filepath.Join(dir, filename)

		err := downloadImage(client, photoURL, destPath)
		if err != nil {
			result.Errors++
			if opts.Logger != nil {
				opts.Logger.Printf("DOWNLOAD_ERROR url=%q dest=%q err=%v", photoURL, destPath, err)
			}
			// Continue with next photo
		} else {
			result.Downloaded++
		}

		// Delay between downloads (skip after last)
		if i < len(urls)-1 {
			jitter := time.Duration(float64(opts.Delay) * 0.2 * rand.Float64())
			time.Sleep(opts.Delay + jitter)
		}
	}

	return result
}

// downloadImage fetches a URL and saves it to dest.
func downloadImage(client *http.Client, url, dest string) error {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return fmt.Errorf("building request: %w", err)
	}
	req.Header.Set("User-Agent", userAgents[rand.IntN(len(userAgents))])
	req.Header.Set("Accept", "image/avif,image/webp,image/apng,image/svg+xml,image/*,*/*;q=0.8")
	req.Header.Set("Referer", "https://www.google.com/")

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("executing request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, resp.Body)
		return fmt.Errorf("unexpected status %d", resp.StatusCode)
	}

	f, err := os.Create(dest)
	if err != nil {
		return fmt.Errorf("creating file: %w", err)
	}
	defer f.Close()

	if _, err := io.Copy(f, resp.Body); err != nil {
		os.Remove(dest)
		return fmt.Errorf("writing file: %w", err)
	}

	return nil
}

// sizeParamRe matches the =sXXX or =sXXX-k-no suffix on Google photo URLs.
var sizeParamRe = regexp.MustCompile(`=[swh]\d+.*$`)

// setResolution replaces the size suffix on a Google photo URL.
func setResolution(photoURL, resolution string) string {
	if idx := strings.LastIndex(photoURL, "="); idx > 0 {
		return photoURL[:idx] + "=" + resolution
	}
	return photoURL + "=" + resolution
}

// sanitizeDirName creates a safe directory name from a business name.
func sanitizeDirName(name string) string {
	// NFD decompose and strip combining marks (accents)
	var b strings.Builder
	for _, r := range norm.NFD.String(name) {
		if unicode.Is(unicode.Mn, r) {
			continue // skip combining marks
		}
		b.WriteRune(r)
	}
	s := b.String()

	s = strings.ToLower(s)

	// Replace problematic characters with underscore
	s = strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '.' {
			return r
		}
		if r == ' ' || r == '_' {
			return '_'
		}
		return -1 // drop
	}, s)

	// Collapse multiple underscores
	for strings.Contains(s, "__") {
		s = strings.ReplaceAll(s, "__", "_")
	}
	s = strings.Trim(s, "_.")

	// Truncate to reasonable length
	if len(s) > 100 {
		s = s[:100]
	}
	return s
}
