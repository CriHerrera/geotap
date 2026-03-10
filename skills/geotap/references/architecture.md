# Architecture

## Directory Structure

```
cmd/geotap/
  main.go               Entry point, CLI dispatcher
  scan.go               Headless scan command
  match.go              Fuzzy match + photo fetch command
  export.go             DB to CSV export command

internal/
  model/
    business.go         Business struct (22 fields incl photos), SearchParams, Sector

  engine/
    geo/
      boundaries.go     Country polygon store (embedded GeoJSON, 177 countries)
      grid.go           Sector grid generation (GenerateGrid, GenerateRadiusGrid)
      filter.go         Land/ocean sector filtering, business geo-filtering
      geocoder.go       Region bounding box geocoding
      geodata/          Embedded ne_110m_countries.geojson (~838KB)

    scraper/
      client.go         HTTP client: utls TLS fingerprint, user agent rotation, backoff
      worker.go         Concurrent scraper: worker pool, stats, geo filter pipeline
      parser_map.go     Google Maps tbm=map response parser
      parser_place.go   Place detail page parser: photo gallery extraction
      pb_template.go    Protobuf parameter builder for search URLs

    storage/
      sqlite.go         SQLite store: InsertBatch (dedup via UNIQUE), UpdatePhotos, Count

  match/
    fuzzy.go            Jaro-Winkler similarity with accent normalization (NFD)

  tui/
    app.go              Root bubbletea model, view routing
    recent.go           Recent projects persistence (~/.config/geotap/recent.json)
    styles/
      theme.go          Color palette and lipgloss styles
    views/
      home.go           Menu: New Search, Load, Recent, Quit
      search.go         Search form with country autocomplete
      progress.go       Live scraping stats and progress bar
      explorer.go       Results browser: table + detail panels + JSON viewer
      filepicker.go     Database file picker
      recent.go         Recently opened databases
    components/
      mapview.go        ASCII map renderer (braille dots)
```

## Data Flow

```
User Input (TUI form or CLI flags)
  → SearchParams struct
  → Geo module generates grid sectors
  → Filter sectors by country polygon (ocean removal)
  → Scraper iterates: sectors x queries (parallel worker pool)
  → Each request: utls TLS → Google Maps tbm=map → parse JSON response
  → Apply filters: rating range → geo polygon containment
  → Store in SQLite (INSERT OR IGNORE for dedup by CID+query)
  → Explorer TUI loads DB → table + detail panels + export
```

## Key Design Decisions

- **Embedded GeoJSON**: 177 countries compiled into binary, no external files needed
- **Pure Go SQLite**: `modernc.org/sqlite` avoids CGO dependency
- **utls TLS**: Mandatory for Google — standard Go TLS gets fingerprinted and blocked
- **Value receiver pattern**: Bubbletea uses value receivers; mutable state behind `*sharedState` pointer
- **Atomic stats**: `sync/atomic.Int64` for thread-safe counters across goroutines
- **Deduplication**: `UNIQUE(cid, query)` constraint + `INSERT OR IGNORE` at DB level
