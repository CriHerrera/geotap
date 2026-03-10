package storage

import (
	"database/sql"
	"fmt"
	"sync"

	_ "modernc.org/sqlite"

	"github.com/rendis/geotap/internal/model"
)

type Store struct {
	db *sql.DB
	mu sync.Mutex
}

func NewStore(dbPath string) (*Store, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("opening db: %w", err)
	}

	// Optimize for write throughput
	pragmas := []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA synchronous=NORMAL",
		"PRAGMA cache_size=-64000",
		"PRAGMA busy_timeout=5000",
	}
	for _, p := range pragmas {
		if _, err := db.Exec(p); err != nil {
			return nil, fmt.Errorf("setting pragma %q: %w", p, err)
		}
	}

	if err := createSchema(db); err != nil {
		return nil, err
	}

	return &Store{db: db}, nil
}

func createSchema(db *sql.DB) error {
	schema := `
	CREATE TABLE IF NOT EXISTS businesses (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		name TEXT NOT NULL,
		rating REAL,
		review_count INTEGER,
		category TEXT,
		address TEXT,
		price_range TEXT,
		lat REAL NOT NULL,
		lng REAL NOT NULL,
		cid TEXT,
		phone TEXT,
		website TEXT,
		google_url TEXT,
		description TEXT,
		place_id TEXT,
		open_hours TEXT,
		thumbnail TEXT,
		categories TEXT,
		city TEXT,
		postal_code TEXT,
		country_code TEXT,
		query TEXT NOT NULL,
		photos TEXT,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		UNIQUE(cid, query)
	);
	CREATE INDEX IF NOT EXISTS idx_businesses_query ON businesses(query);
	CREATE INDEX IF NOT EXISTS idx_businesses_rating ON businesses(rating);
	CREATE INDEX IF NOT EXISTS idx_businesses_coords ON businesses(lat, lng);
	CREATE INDEX IF NOT EXISTS idx_businesses_city ON businesses(city);
	`
	_, err := db.Exec(schema)
	if err != nil {
		return fmt.Errorf("creating schema: %w", err)
	}
	return nil
}

func (s *Store) InsertBatch(businesses []model.Business) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.db.Begin()
	if err != nil {
		return 0, fmt.Errorf("beginning tx: %w", err)
	}

	stmt, err := tx.Prepare(`
		INSERT OR IGNORE INTO businesses
		(name, rating, review_count, category, address, price_range, lat, lng, cid,
		 phone, website, google_url, description, place_id,
		 open_hours, thumbnail, categories, city, postal_code, country_code, query, photos)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
	`)
	if err != nil {
		tx.Rollback()
		return 0, fmt.Errorf("preparing stmt: %w", err)
	}
	defer stmt.Close()

	inserted := 0
	for _, b := range businesses {
		res, err := stmt.Exec(
			b.Name, b.Rating, b.ReviewCount, b.Category, b.Address, b.PriceRange,
			b.Lat, b.Lng, b.CID, b.Phone, b.Website,
			b.GoogleURL, b.Description, b.PlaceID,
			b.OpenHours, b.Thumbnail, b.Categories,
			b.City, b.PostalCode, b.CountryCode, b.Query, b.Photos,
		)
		if err != nil {
			continue
		}
		n, _ := res.RowsAffected()
		inserted += int(n)
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("committing tx: %w", err)
	}

	return inserted, nil
}

// UpdatePhotos sets the photos JSON array for a business identified by place_id and query.
func (s *Store) UpdatePhotos(placeID, query, photosJSON string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec("UPDATE businesses SET photos = ? WHERE place_id = ? AND query = ?", photosJSON, placeID, query)
	return err
}

func (s *Store) Count() (int, error) {
	var count int
	err := s.db.QueryRow("SELECT COUNT(*) FROM businesses").Scan(&count)
	return count, err
}

func (s *Store) Close() error {
	return s.db.Close()
}
