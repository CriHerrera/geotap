package model

// Sector represents a grid cell for geographic searching.
type Sector struct {
	Lat  float64
	Lng  float64
	Span float64 // degrees covered by this sector
	Row  int
	Col  int
}

// Business represents a scraped business from Google Maps.
type Business struct {
	Name        string  `json:"name"`
	Rating      float64 `json:"rating"`
	ReviewCount int     `json:"review_count"`
	Category    string  `json:"category"`
	Address     string  `json:"address"`
	PriceRange  string  `json:"price_range"`
	Lat         float64 `json:"lat"`
	Lng         float64 `json:"lng"`
	CID         string  `json:"cid"`
	Phone       string  `json:"phone"`
	Website     string  `json:"website"`
	GoogleURL   string  `json:"google_url"`
	Description string  `json:"description"`
	PlaceID     string  `json:"place_id"`
	OpenHours   string  `json:"open_hours"`
	Thumbnail   string  `json:"thumbnail"`
	Photos      string  `json:"photos"`
	Categories  string  `json:"categories"`
	City        string  `json:"city"`
	PostalCode  string  `json:"postal_code"`
	CountryCode string  `json:"country_code"`
	Query       string  `json:"query"`
}

// SearchParams holds all configuration for a scraping session.
type SearchParams struct {
	// Mode 1: By country/region
	Country  string
	Region   string
	Province string
	City     string

	// Mode 2: By coordinates
	Lat    float64
	Lng    float64
	Radius float64 // km

	// Common
	Queries     []string
	Zoom        int
	Concurrency int
	MaxPages    int     // max pagination pages per sector (default 1)
	MinRating   float64 // min star filter (0 = no filter)
	MaxRating   float64 // max star filter (0 = no filter)
	DBPath      string
	Lang        string // hl parameter (default "es")
	ProxyURL    string // HTTP/SOCKS5 proxy URL (optional)
	FetchPhotos bool   // fetch all photo URLs per business
	Debug       bool
}

func (p *SearchParams) IsCoordMode() bool {
	return p.Lat != 0 || p.Lng != 0
}
