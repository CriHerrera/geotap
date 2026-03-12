package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/rendis/geotap/internal/engine/geo"
	"github.com/rendis/geotap/internal/engine/scraper"
	"github.com/rendis/geotap/internal/engine/storage"
	"github.com/rendis/geotap/internal/match"
	"github.com/rendis/geotap/internal/model"
)

// loadStopWords reads a stop words file (one word per line, already normalized).
func loadStopWords(path string) (map[string]bool, error) {
	if path == "" {
		return nil, nil
	}

	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("opening stop words file: %w", err)
	}
	defer f.Close()

	sw := make(map[string]bool)
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		word := strings.TrimSpace(scanner.Text())
		if word != "" && !strings.HasPrefix(word, "#") {
			sw[word] = true
		}
	}
	return sw, scanner.Err()
}

func runMatch(args []string) error {
	var (
		lat, lng, radius float64
		query            string
		name             string
		threshold        float64
		zoom             int
		concurrency      int
		maxPages         int
		lang             string
		proxyURL         string
		outputDir        string
		photoDelay       float64
		noPhotos         bool
		noDownload       bool
		maxPhotos        int
		photoResolution  string
		stopWordsFile    string
		debug            bool
	)

	fs := flag.NewFlagSet("match", flag.ExitOnError)
	fs.Float64Var(&lat, "lat", 0, "Center latitude (required)")
	fs.Float64Var(&lng, "lng", 0, "Center longitude (required)")
	fs.Float64Var(&radius, "radius", 0.5, "Search radius in km (default 0.5)")
	fs.StringVar(&query, "query", "escuela", "Google Maps search query (default: escuela)")
	fs.StringVar(&name, "name", "", "Name to fuzzy-match against (required)")
	fs.Float64Var(&threshold, "threshold", 90, "Minimum similarity % to consider a match (default 90)")
	fs.IntVar(&zoom, "zoom", 16, "Zoom level 10-16 (default 16)")
	fs.IntVar(&concurrency, "concurrency", 5, "Max concurrent requests")
	fs.IntVar(&maxPages, "max-pages", 1, "Max pagination pages per sector")
	fs.StringVar(&lang, "lang", "es", "Search language")
	fs.StringVar(&proxyURL, "proxy", "", "HTTP/SOCKS5 proxy URL")
	fs.StringVar(&outputDir, "output", "", "Output directory (required)")
	fs.Float64Var(&photoDelay, "photo-delay", 1.5, "Delay in seconds between photo requests (default 1.5)")
	fs.BoolVar(&noPhotos, "no-photos", false, "Skip photo fetching (Phase 3)")
	fs.BoolVar(&noDownload, "no-download", false, "Skip photo download to disk (Phase 4)")
	fs.IntVar(&maxPhotos, "max-photos", 50, "Max photos to download per business (default 50)")
	fs.StringVar(&photoResolution, "photo-resolution", "s1200-k-no", "Photo size suffix (e.g. s1200-k-no, s0 for original)")
	fs.StringVar(&stopWordsFile, "stop-words", "", "Path to stop words file (one word per line, normalized)")
	fs.BoolVar(&debug, "debug", false, "Dump raw responses to files")

	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: geotap match [flags]\n\n")
		fmt.Fprintf(os.Stderr, "Search for places near coordinates, fuzzy-match by name, and fetch photos for matches.\n\n")
		fmt.Fprintf(os.Stderr, "Flags:\n")
		fs.PrintDefaults()
		fmt.Fprintf(os.Stderr, "\nExamples:\n")
		fmt.Fprintf(os.Stderr, "  geotap match -lat -33.45 -lng -70.66 -name \"Colegio San Jose\" -output ./results\n")
		fmt.Fprintf(os.Stderr, "  geotap match -lat 6.25 -lng -75.60 -query \"colegios\" -name \"Escuela Pedro De Castro\" -stop-words stopwords.txt -output ./results\n")
	}

	if err := fs.Parse(args); err != nil {
		return err
	}

	// Validation
	if lat == 0 && lng == 0 {
		return fmt.Errorf("-lat and -lng are required")
	}
	if name == "" {
		return fmt.Errorf("-name is required")
	}
	if outputDir == "" {
		return fmt.Errorf("-output is required")
	}

	// Normalize threshold to 0.0-1.0
	thresholdNorm := threshold / 100.0

	// Load stop words
	stopWords, err := loadStopWords(stopWordsFile)
	if err != nil {
		return err
	}
	if len(stopWords) > 0 {
		fmt.Fprintf(os.Stderr, "Stop words: %d loaded from %s\n", len(stopWords), stopWordsFile)
	}

	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return fmt.Errorf("creating output dir: %w", err)
	}

	ts := time.Now().Format("20060102_150405")
	baseName := fmt.Sprintf("geotap_match_%s", ts)
	dbPath := filepath.Join(outputDir, baseName+".db")
	logPath := filepath.Join(outputDir, baseName+".log")

	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return fmt.Errorf("opening log: %w", err)
	}
	defer logFile.Close()
	logger := log.New(logFile, "", log.LstdFlags)
	logger.Printf("=== Match session: name=%q query=%q lat=%.4f lng=%.4f radius=%.1fkm threshold=%.0f%% stopWords=%d ===",
		name, query, lat, lng, radius, threshold, len(stopWords))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		fmt.Fprintln(os.Stderr, "\nShutting down gracefully...")
		cancel()
	}()

	// Phase 1: Search for places in radius
	fmt.Fprintf(os.Stderr, "Phase 1: Searching %q within %.1fkm of (%.4f, %.4f)\n", query, radius, lat, lng)

	sectors := geo.GenerateRadiusGrid(lat, lng, radius, zoom)
	fmt.Fprintf(os.Stderr, "Grid: %d sectors\n", len(sectors))

	if len(sectors) == 0 {
		return fmt.Errorf("no sectors generated for radius %.1fkm", radius)
	}

	store, err := storage.NewStore(dbPath)
	if err != nil {
		return fmt.Errorf("opening store: %w", err)
	}
	defer store.Close()

	params := model.SearchParams{
		Lat:         lat,
		Lng:         lng,
		Radius:      radius,
		Queries:     []string{query},
		Zoom:        zoom,
		Concurrency: concurrency,
		MaxPages:    maxPages,
		Lang:        lang,
		ProxyURL:    proxyURL,
		DBPath:      dbPath,
		Debug:       debug,
	}

	stats, err := scraper.Run(ctx, sectors, params, store, logger, &scraper.RunOptions{
		SuppressStderr: true,
	})
	if err != nil && err != context.Canceled {
		return fmt.Errorf("scraping: %w", err)
	}

	total, _ := store.Count()
	fmt.Fprintf(os.Stderr, "Found %d places (%d unique)\n\n", stats.BusinessesFound.Load(), total)

	if total == 0 {
		fmt.Fprintf(os.Stderr, "No results found. Try a larger radius or different query.\n")
		return nil
	}

	// Phase 2: Load results and fuzzy match
	fmt.Fprintf(os.Stderr, "Phase 2: Fuzzy matching against %q (threshold: %.0f%%)\n", name, threshold)

	allBusinesses, err := loadMatchResults(dbPath)
	if err != nil {
		return fmt.Errorf("loading results: %w", err)
	}

	// Filter businesses within the search radius
	var businesses []model.Business
	for _, b := range allBusinesses {
		if b.Lat == 0 && b.Lng == 0 {
			continue
		}
		if geo.HaversineKm(lat, lng, b.Lat, b.Lng) <= radius {
			businesses = append(businesses, b)
		}
	}
	fmt.Fprintf(os.Stderr, "Filtered: %d of %d places within %.1fkm radius\n", len(businesses), len(allBusinesses), radius)

	// Show normalized target name for debugging
	nameClean := match.RemoveStopWords(match.Normalize(name), stopWords)
	fmt.Fprintf(os.Stderr, "Target (clean): %q\n", nameClean)

	type matchResult struct {
		Business   model.Business
		Similarity float64
	}

	var matches []matchResult
	for _, b := range businesses {
		sim := match.Similarity(name, b.Name, stopWords)
		pct := sim * 100
		bClean := match.RemoveStopWords(match.Normalize(b.Name), stopWords)
		if sim >= thresholdNorm {
			matches = append(matches, matchResult{Business: b, Similarity: sim})
			fmt.Fprintf(os.Stderr, "  MATCH  %.0f%%  %s  [%s]\n", pct, b.Name, bClean)
		} else {
			fmt.Fprintf(os.Stderr, "         %.0f%%  %s  [%s]\n", pct, b.Name, bClean)
		}
		logger.Printf("FUZZY name=%q candidate=%q name_clean=%q candidate_clean=%q similarity=%.2f matched=%v",
			name, b.Name, nameClean, bClean, sim, sim >= thresholdNorm)
	}

	fmt.Fprintf(os.Stderr, "\n%d matches out of %d places\n", len(matches), len(businesses))

	if len(matches) == 0 {
		fmt.Fprintf(os.Stderr, "No matches above %.0f%% threshold. Try lowering -threshold.\n", threshold)
		return nil
	}

	// Find the best match (highest similarity)
	bestIdx := 0
	for i, m := range matches {
		if m.Similarity > matches[bestIdx].Similarity {
			bestIdx = i
		}
	}
	best := matches[bestIdx]
	fmt.Fprintf(os.Stderr, "\nBest match: %s (%.0f%%)\n", best.Business.Name, best.Similarity*100)

	if best.Similarity < thresholdNorm {
		fmt.Fprintf(os.Stderr, "Best match below %.0f%% threshold — skipping photo download.\n", threshold)
		return nil
	}

	client := scraper.NewClient(lang, proxyURL, zoom)

	// Phase 3: Extract gallery photos using headless Chrome for the best match
	photosDir := filepath.Join(outputDir, "fotos")
	totalDownloaded := 0
	var galleryPhotos []string

	if noPhotos {
		fmt.Fprintf(os.Stderr, "\nPhase 3: Skipped (--no-photos)\n")
	} else {
		if best.Business.PlaceID == "" {
			fmt.Fprintf(os.Stderr, "\nPhase 3: Skipped (best match has no place_id)\n")
		} else {
			fmt.Fprintf(os.Stderr, "\nPhase 3: Extracting gallery photos via headless Chrome for %q...\n", best.Business.Name)

			var err error
			debugGalleryDir := ""
			if debug {
				debugGalleryDir = filepath.Join(outputDir, "debug_gallery")
			}
			galleryPhotos, err = scraper.FetchGalleryPhotos(ctx, best.Business.PlaceID, maxPhotos, debugGalleryDir, logger)
			if err != nil {
				fmt.Fprintf(os.Stderr, "  Gallery error: %v\n", err)
				logger.Printf("GALLERY_ERROR place_id=%s err=%v", best.Business.PlaceID, err)
			} else {
				fmt.Fprintf(os.Stderr, "  Found %d gallery photos\n", len(galleryPhotos))
				logger.Printf("GALLERY place_id=%s name=%q count=%d", best.Business.PlaceID, best.Business.Name, len(galleryPhotos))
			}

			// Save photo URLs to DB
			if len(galleryPhotos) > 0 {
				photosJSON, _ := json.Marshal(galleryPhotos)
				if err := store.UpdatePhotos(best.Business.PlaceID, query, string(photosJSON)); err != nil {
					logger.Printf("PHOTO_DB_ERROR place_id=%s err=%v", best.Business.PlaceID, err)
				}
			}
		}
	}

	// Phase 4: Download gallery photos to disk
	if noPhotos || noDownload || len(galleryPhotos) == 0 {
		if noPhotos || noDownload {
			fmt.Fprintf(os.Stderr, "\nPhase 4: Skipped (--no-download)\n")
		} else if len(galleryPhotos) == 0 {
			fmt.Fprintf(os.Stderr, "\nPhase 4: No photos to download\n")
		}
	} else {
		fmt.Fprintf(os.Stderr, "\nPhase 4: Downloading %d photos to disk (resolution=%s)\n", len(galleryPhotos), photoResolution)

		downloadClient := client.HTTPClient()
		dlDelay := time.Duration(photoDelay * float64(time.Second))

		result := scraper.DownloadPlacePhotos(downloadClient, best.Business.Name, galleryPhotos, scraper.DownloadOptions{
			OutputDir:  photosDir,
			MaxPhotos:  maxPhotos,
			Resolution: photoResolution,
			Delay:      dlDelay,
			Logger:     logger,
		})

		totalDownloaded = result.Downloaded
		fmt.Fprintf(os.Stderr, "  %d ok, %d errors → %s\n", result.Downloaded, result.Errors, result.Dir)
		logger.Printf("DOWNLOAD name=%q downloaded=%d errors=%d dir=%q", best.Business.Name, result.Downloaded, result.Errors, result.Dir)
	}

	// Summary
	fmt.Fprintf(os.Stderr, "\n==============================\n")
	fmt.Fprintf(os.Stderr, "  GeoTap Match Complete\n")
	fmt.Fprintf(os.Stderr, "==============================\n")
	fmt.Fprintf(os.Stderr, "  Target:     %s\n", name)
	fmt.Fprintf(os.Stderr, "  Query:      %s\n", query)
	fmt.Fprintf(os.Stderr, "  Center:     %.4f, %.4f (r=%.1fkm)\n", lat, lng, radius)
	fmt.Fprintf(os.Stderr, "  Found:      %d places\n", total)
	fmt.Fprintf(os.Stderr, "  Matches:    %d (>%.0f%%)\n", len(matches), threshold)
	if !noPhotos && !noDownload {
		fmt.Fprintf(os.Stderr, "  Photos:     %d downloaded → %s\n", totalDownloaded, photosDir)
	}
	fmt.Fprintf(os.Stderr, "  Database:   %s\n", dbPath)
	fmt.Fprintf(os.Stderr, "  Log:        %s\n", logPath)
	fmt.Fprintf(os.Stderr, "==============================\n")

	return nil
}

// loadMatchResults loads all businesses from a database file.
func loadMatchResults(dbPath string) ([]model.Business, error) {
	// Reuse the same loader from export command
	return loadFromDB(dbPath)
}
