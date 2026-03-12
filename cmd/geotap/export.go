package main

import (
	"database/sql"
	"encoding/csv"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite"

	"github.com/rendis/geotap/internal/model"
)

func runExport(args []string) error {
	var dbPath, outputPath, format string

	fs := flag.NewFlagSet("export", flag.ExitOnError)
	fs.StringVar(&dbPath, "db", "", "Path to .db file (required)")
	fs.StringVar(&outputPath, "output", "", "Output file path (default: same dir as db)")
	fs.StringVar(&format, "format", "csv", "Export format: csv")

	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: geotap export [flags]\n\nFlags:\n")
		fs.PrintDefaults()
		fmt.Fprintf(os.Stderr, "\nExamples:\n")
		fmt.Fprintf(os.Stderr, "  geotap export -db ./projects/geotap_20260212.db\n")
		fmt.Fprintf(os.Stderr, "  geotap export -db data.db -output results.csv\n")
	}

	if err := fs.Parse(args); err != nil {
		return err
	}

	if dbPath == "" {
		return fmt.Errorf("-db is required")
	}

	if format != "csv" {
		return fmt.Errorf("unsupported format: %s (only csv supported)", format)
	}

	// Default output path
	if outputPath == "" {
		dir := filepath.Dir(dbPath)
		base := strings.TrimSuffix(filepath.Base(dbPath), ".db")
		outputPath = filepath.Join(dir, base+".csv")
	}

	// Load businesses
	businesses, err := loadFromDB(dbPath)
	if err != nil {
		return fmt.Errorf("loading db: %w", err)
	}

	if len(businesses) == 0 {
		return fmt.Errorf("no businesses found in database")
	}

	// Export
	f, err := os.Create(outputPath)
	if err != nil {
		return fmt.Errorf("creating output: %w", err)
	}
	defer f.Close()

	w := csv.NewWriter(f)
	defer w.Flush()

	w.Write([]string{
		"name", "rating", "review_count", "category", "categories",
		"address", "city", "postal_code", "country_code",
		"lat", "lng", "phone", "website", "google_url",
		"description", "price_range", "thumbnail", "photos", "query",
	})

	for _, b := range businesses {
		w.Write([]string{
			b.Name,
			fmt.Sprintf("%.1f", b.Rating),
			fmt.Sprintf("%d", b.ReviewCount),
			b.Category,
			b.Categories,
			b.Address,
			b.City,
			b.PostalCode,
			b.CountryCode,
			fmt.Sprintf("%.6f", b.Lat),
			fmt.Sprintf("%.6f", b.Lng),
			b.Phone,
			b.Website,
			b.GoogleURL,
			b.Description,
			b.PriceRange,
			b.Thumbnail,
			b.Photos,
			b.Query,
		})
	}

	fmt.Fprintf(os.Stderr, "Exported %d businesses to %s\n", len(businesses), outputPath)
	return nil
}

func loadFromDB(dbPath string) ([]model.Business, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, err
	}
	defer db.Close()

	rows, err := db.Query(`
		SELECT name, rating, review_count, category, address, price_range,
		       lat, lng, cid, phone, website, google_url, description, place_id,
		       open_hours, thumbnail, COALESCE(photos, '') as photos,
		       categories, city, postal_code, country_code, query
		FROM businesses ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var businesses []model.Business
	for rows.Next() {
		var b model.Business
		err := rows.Scan(
			&b.Name, &b.Rating, &b.ReviewCount, &b.Category, &b.Address, &b.PriceRange,
			&b.Lat, &b.Lng, &b.CID, &b.Phone, &b.Website, &b.GoogleURL, &b.Description, &b.PlaceID,
			&b.OpenHours, &b.Thumbnail, &b.Photos, &b.Categories, &b.City, &b.PostalCode, &b.CountryCode, &b.Query,
		)
		if err != nil {
			continue
		}
		businesses = append(businesses, b)
	}
	return businesses, nil
}
