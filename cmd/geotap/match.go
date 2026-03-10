package main

import (
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
		debug            bool
	)

	fs := flag.NewFlagSet("match", flag.ExitOnError)
	fs.Float64Var(&lat, "lat", 0, "Center latitude (required)")
	fs.Float64Var(&lng, "lng", 0, "Center longitude (required)")
	fs.Float64Var(&radius, "radius", 0.5, "Search radius in km (default 0.5)")
	fs.StringVar(&query, "query", "schools", "Google Maps search query (default: schools)")
	fs.StringVar(&name, "name", "", "Name to fuzzy-match against (required)")
	fs.Float64Var(&threshold, "threshold", 50, "Minimum similarity % to consider a match (default 50)")
	fs.IntVar(&zoom, "zoom", 16, "Zoom level 10-16 (default 16)")
	fs.IntVar(&concurrency, "concurrency", 5, "Max concurrent requests")
	fs.IntVar(&maxPages, "max-pages", 1, "Max pagination pages per sector")
	fs.StringVar(&lang, "lang", "en", "Search language")
	fs.StringVar(&proxyURL, "proxy", "", "HTTP/SOCKS5 proxy URL")
	fs.StringVar(&outputDir, "output", "", "Output directory (required)")
	fs.Float64Var(&photoDelay, "photo-delay", 1.5, "Delay in seconds between photo requests (default 1.5)")
	fs.BoolVar(&debug, "debug", false, "Dump raw responses to files")

	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: geotap match [flags]\n\n")
		fmt.Fprintf(os.Stderr, "Search for places near coordinates, fuzzy-match by name, and fetch photos for matches.\n\n")
		fmt.Fprintf(os.Stderr, "Flags:\n")
		fs.PrintDefaults()
		fmt.Fprintf(os.Stderr, "\nExamples:\n")
		fmt.Fprintf(os.Stderr, "  geotap match -lat -33.45 -lng -70.66 -name \"Colegio San José\" -output ./results\n")
		fmt.Fprintf(os.Stderr, "  geotap match -lat 40.42 -lng -3.70 -query \"schools\" -name \"IES Cervantes\" -threshold 60 -output ./results\n")
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
	logger.Printf("=== Match session: name=%q query=%q lat=%.4f lng=%.4f radius=%.1fkm threshold=%.0f%% ===",
		name, query, lat, lng, radius, threshold)

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

	businesses, err := loadMatchResults(dbPath)
	if err != nil {
		return fmt.Errorf("loading results: %w", err)
	}

	type matchResult struct {
		Business   model.Business
		Similarity float64
	}

	var matches []matchResult
	for _, b := range businesses {
		sim := match.Similarity(name, b.Name)
		pct := sim * 100
		if sim >= thresholdNorm {
			matches = append(matches, matchResult{Business: b, Similarity: sim})
			fmt.Fprintf(os.Stderr, "  MATCH  %.0f%%  %s\n", pct, b.Name)
		} else {
			fmt.Fprintf(os.Stderr, "         %.0f%%  %s\n", pct, b.Name)
		}
		logger.Printf("FUZZY name=%q candidate=%q similarity=%.2f matched=%v", name, b.Name, sim, sim >= thresholdNorm)
	}

	fmt.Fprintf(os.Stderr, "\n%d matches out of %d places\n", len(matches), len(businesses))

	if len(matches) == 0 {
		fmt.Fprintf(os.Stderr, "No matches above %.0f%% threshold. Try lowering -threshold.\n", threshold)
		return nil
	}

	// Phase 3: Fetch photos for matched places
	fmt.Fprintf(os.Stderr, "\nPhase 3: Fetching photos for %d matched places\n", len(matches))

	client := scraper.NewClient(lang, proxyURL, zoom)
	delay := time.Duration(photoDelay * float64(time.Second))

	for i, m := range matches {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		if m.Business.PlaceID == "" {
			fmt.Fprintf(os.Stderr, "  [%d/%d] %s — no place_id, skipping photos\n", i+1, len(matches), m.Business.Name)
			continue
		}

		fmt.Fprintf(os.Stderr, "  [%d/%d] %s — fetching photos...", i+1, len(matches), m.Business.Name)

		body, err := client.FetchPlace(m.Business.PlaceID)
		if err != nil {
			fmt.Fprintf(os.Stderr, " error: %v\n", err)
			logger.Printf("PHOTO_ERROR place_id=%s err=%v", m.Business.PlaceID, err)
			continue
		}

		if debug {
			debugFile := filepath.Join(outputDir, fmt.Sprintf("debug_place_%s.html", m.Business.PlaceID))
			os.WriteFile(debugFile, body, 0644)
		}

		photos := scraper.ParsePlacePhotos(body)
		fmt.Fprintf(os.Stderr, " %d photos found\n", len(photos))
		logger.Printf("PHOTOS place_id=%s name=%q count=%d", m.Business.PlaceID, m.Business.Name, len(photos))

		if len(photos) > 0 {
			photosJSON, _ := json.Marshal(photos)
			if err := store.UpdatePhotos(m.Business.PlaceID, query, string(photosJSON)); err != nil {
				logger.Printf("PHOTO_DB_ERROR place_id=%s err=%v", m.Business.PlaceID, err)
			}
		}

		// Delay between requests to avoid rate limiting
		if i < len(matches)-1 {
			time.Sleep(delay)
		}
	}

	// Summary
	fmt.Fprintf(os.Stderr, "\n══════════════════════════════\n")
	fmt.Fprintf(os.Stderr, "  GeoTap Match Complete\n")
	fmt.Fprintf(os.Stderr, "══════════════════════════════\n")
	fmt.Fprintf(os.Stderr, "  Target:     %s\n", name)
	fmt.Fprintf(os.Stderr, "  Query:      %s\n", query)
	fmt.Fprintf(os.Stderr, "  Center:     %.4f, %.4f (r=%.1fkm)\n", lat, lng, radius)
	fmt.Fprintf(os.Stderr, "  Found:      %d places\n", total)
	fmt.Fprintf(os.Stderr, "  Matches:    %d (>%.0f%%)\n", len(matches), threshold)
	fmt.Fprintf(os.Stderr, "  Database:   %s\n", dbPath)
	fmt.Fprintf(os.Stderr, "  Log:        %s\n", logPath)
	fmt.Fprintf(os.Stderr, "══════════════════════════════\n")

	return nil
}

// loadMatchResults loads all businesses from a database file.
func loadMatchResults(dbPath string) ([]model.Business, error) {
	// Reuse the same loader from export command
	return loadFromDB(dbPath)
}
