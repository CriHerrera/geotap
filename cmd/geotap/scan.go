package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/paulmach/orb"
	"github.com/rendis/geotap/internal/engine/geo"
	"github.com/rendis/geotap/internal/engine/scraper"
	"github.com/rendis/geotap/internal/engine/storage"
	"github.com/rendis/geotap/internal/model"
	"github.com/rendis/geotap/internal/tui"
)

func runScan(args []string) error {
	var params model.SearchParams
	var queriesStr, outputDir string

	fs := flag.NewFlagSet("scan", flag.ExitOnError)
	fs.StringVar(&outputDir, "output", "", "Output directory for project files (required)")
	fs.StringVar(&params.Country, "country", "", "Country name or ISO code")
	fs.StringVar(&params.Region, "region", "", "Region/state (optional)")
	fs.StringVar(&params.Province, "province", "", "Province (optional)")
	fs.StringVar(&params.City, "city", "", "City (optional)")
	fs.Float64Var(&params.Lat, "lat", 0, "Center latitude")
	fs.Float64Var(&params.Lng, "lng", 0, "Center longitude")
	fs.Float64Var(&params.Radius, "radius", 10, "Search radius in km")
	fs.StringVar(&queriesStr, "queries", "", "Comma-separated search terms (required)")
	fs.IntVar(&params.Zoom, "zoom", 0, "Zoom level 10-16 (default: auto)")
	fs.IntVar(&params.Concurrency, "concurrency", 10, "Max concurrent requests")
	fs.IntVar(&params.MaxPages, "max-pages", 1, "Max pagination pages per sector")
	fs.Float64Var(&params.MinRating, "min-rating", 0, "Minimum star rating filter")
	fs.Float64Var(&params.MaxRating, "max-rating", 0, "Maximum star rating filter")
	fs.StringVar(&params.Lang, "lang", "en", "Search language")
	fs.StringVar(&params.ProxyURL, "proxy", "", "HTTP/SOCKS5 proxy URL")
	fs.BoolVar(&params.FetchPhotos, "photos", false, "Fetch all photo URLs per business (slower)")
	fs.BoolVar(&params.Debug, "debug", false, "Dump raw responses")

	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: geotap scan [flags]\n\nFlags:\n")
		fs.PrintDefaults()
		fmt.Fprintf(os.Stderr, "\nExamples:\n")
		fmt.Fprintf(os.Stderr, "  geotap scan -queries restaurants -country Chile -output ./projects\n")
		fmt.Fprintf(os.Stderr, "  geotap scan -queries \"cafes,bars\" -lat 40.4168 -lng -3.7038 -radius 5 -output ./projects\n")
	}

	if err := fs.Parse(args); err != nil {
		return err
	}

	// Validation
	if queriesStr == "" {
		return fmt.Errorf("-queries is required")
	}
	if outputDir == "" {
		return fmt.Errorf("-output is required")
	}
	if !params.IsCoordMode() && params.Country == "" {
		return fmt.Errorf("either -country or -lat/-lng is required")
	}

	params.Queries = strings.Split(queriesStr, ",")
	for i := range params.Queries {
		params.Queries[i] = strings.TrimSpace(params.Queries[i])
	}

	// Smart zoom default
	if params.Zoom == 0 {
		if params.IsCoordMode() {
			params.Zoom = 13
		} else {
			params.Zoom = 10
		}
	}

	// Create output directory
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return fmt.Errorf("creating output dir: %w", err)
	}

	// Generate timestamped filenames
	ts := time.Now().Format("20060102_150405")
	baseName := fmt.Sprintf("geotap_%s", ts)
	params.DBPath = filepath.Join(outputDir, baseName+".db")
	logPath := filepath.Join(outputDir, baseName+".log")

	// Setup log file
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return fmt.Errorf("opening log: %w", err)
	}
	defer logFile.Close()
	logger := log.New(logFile, "", log.LstdFlags)
	logger.Printf("=== Session start: queries=%v country=%s lat=%.4f lng=%.4f radius=%.1f zoom=%d concurrency=%d ===",
		params.Queries, params.Country, params.Lat, params.Lng, params.Radius, params.Zoom, params.Concurrency)

	fmt.Fprintf(os.Stderr, "Log: %s\n", logPath)

	// Setup context with graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		fmt.Fprintln(os.Stderr, "\nShutting down gracefully...")
		cancel()
	}()

	// Generate sectors
	startTime := time.Now()
	var sectors []model.Sector
	var poly orb.MultiPolygon

	if params.IsCoordMode() {
		fmt.Fprintf(os.Stderr, "Mode: coordinate search (%.4f, %.4f, radius=%.1fkm)\n",
			params.Lat, params.Lng, params.Radius)
		sectors = geo.GenerateRadiusGrid(params.Lat, params.Lng, params.Radius, params.Zoom)
		fmt.Fprintf(os.Stderr, "Grid: %d sectors within radius\n", len(sectors))
	} else {
		fmt.Fprintf(os.Stderr, "Mode: country search (%s)\n", params.Country)

		bs, err := geo.NewBoundaryStore()
		if err != nil {
			return fmt.Errorf("loading boundaries: %w", err)
		}

		var minLat, minLng, maxLat, maxLng float64
		if params.Region != "" {
			fmt.Fprintf(os.Stderr, "Region: %s\n", params.Region)
			minLat, minLng, maxLat, maxLng, err = geo.GeocodeRegion(params.Region, params.Country)
			if err != nil {
				return fmt.Errorf("geocoding region %q: %w", params.Region, err)
			}
		} else {
			minLat, minLng, maxLat, maxLng, err = bs.GetCountryBounds(params.Country)
			if err != nil {
				return fmt.Errorf("getting bounds: %w", err)
			}
		}
		fmt.Fprintf(os.Stderr, "Bounds: [%.2f, %.2f] - [%.2f, %.2f]\n", minLat, minLng, maxLat, maxLng)

		allSectors := geo.GenerateGrid(minLat, minLng, maxLat, maxLng, params.Zoom)
		fmt.Fprintf(os.Stderr, "Grid: %d total sectors\n", len(allSectors))

		poly, err = bs.GetCountryPolygon(params.Country)
		if err != nil {
			return fmt.Errorf("getting polygon: %w", err)
		}
		sectors = geo.FilterLandSectors(allSectors, poly)
		oceanPct := 100.0 * float64(len(allSectors)-len(sectors)) / float64(len(allSectors))
		fmt.Fprintf(os.Stderr, "GeoFilter: %d land sectors (%.1f%% ocean removed)\n", len(sectors), oceanPct)
	}

	if len(sectors) == 0 {
		return fmt.Errorf("no sectors to process")
	}

	// Open storage
	store, err := storage.NewStore(params.DBPath)
	if err != nil {
		return fmt.Errorf("opening store: %w", err)
	}
	defer store.Close()

	// Run scraper
	totalJobs := len(params.Queries) * len(sectors)
	fmt.Fprintf(os.Stderr, "Scraping: %d queries x %d sectors = %d jobs (concurrency=%d)\n",
		len(params.Queries), len(sectors), totalJobs, params.Concurrency)
	logger.Printf("Scraping: %d jobs (%d queries x %d sectors), concurrency=%d",
		totalJobs, len(params.Queries), len(sectors), params.Concurrency)

	stats, err := scraper.Run(ctx, sectors, params, store, logger, &scraper.RunOptions{
		GeoFilter: poly,
	})
	if err != nil && err != context.Canceled {
		return fmt.Errorf("scraping: %w", err)
	}

	duration := time.Since(startTime).Truncate(time.Second)
	total, _ := store.Count()

	logger.Printf("Done: found=%d stored=%d errors=%d rate_limits=%d total_in_db=%d",
		stats.BusinessesFound.Load(), stats.BusinessesStored.Load(),
		stats.Errors.Load(), stats.RateLimits.Load(), total)

	// Print final summary
	fmt.Fprintf(os.Stderr, "\n")
	fmt.Fprintf(os.Stderr, "══════════════════════════════\n")
	fmt.Fprintf(os.Stderr, "  GeoTap Complete\n")
	fmt.Fprintf(os.Stderr, "══════════════════════════════\n")
	fmt.Fprintf(os.Stderr, "  Query:      %s\n", strings.Join(params.Queries, ", "))
	if params.Country != "" {
		fmt.Fprintf(os.Stderr, "  Country:    %s\n", params.Country)
	} else {
		fmt.Fprintf(os.Stderr, "  Center:     %.4f, %.4f (r=%.1fkm)\n", params.Lat, params.Lng, params.Radius)
	}
	fmt.Fprintf(os.Stderr, "  Sectors:    %d\n", len(sectors)*len(params.Queries))
	fmt.Fprintf(os.Stderr, "  Found:      %d\n", stats.BusinessesFound.Load())
	fmt.Fprintf(os.Stderr, "  Stored:     %d (unique)\n", total)
	fmt.Fprintf(os.Stderr, "  Errors:     %d\n", stats.Errors.Load())
	fmt.Fprintf(os.Stderr, "  Duration:   %s\n", duration)
	fmt.Fprintf(os.Stderr, "  Database:   %s\n", params.DBPath)
	fmt.Fprintf(os.Stderr, "  Log:        %s\n", logPath)
	fmt.Fprintf(os.Stderr, "══════════════════════════════\n")

	tui.SaveRecent(params.DBPath)

	return nil
}
