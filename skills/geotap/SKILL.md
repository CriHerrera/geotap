---
name: geotap
description: >-
  Google Maps geographic data scanner. Scrapes business listings by country,
  region, or coordinates without API key or login. TLS fingerprinting and
  anti-blocking. CLI and interactive TUI modes. Exports to SQLite and CSV.
  Use when scraping Google Maps, extracting business data, geographic grid
  generation, or running geotap CLI/TUI commands.
license: MIT
compatibility: >
  Requires Go 1.24+. Runs on macOS (arm64/amd64), Linux (amd64/arm64),
  and Windows (amd64). No CGO required (pure Go SQLite).
metadata:
  version: "0.1.0"
  author: "rendis"
  repository: "https://github.com/rendis/geotap"
  platforms: "darwin linux windows"
  openclaw-emoji: "📍"
  openclaw-os: "darwin linux windows"
  openclaw-user-invocable: "true"
  openclaw-install-type: "go"
  openclaw-install-package: "github.com/rendis/geotap/cmd/geotap"
---

# GeoTap

Google Maps geographic data scanner. No API key, no login, anti-blocking built-in.

## Quick Start

Install:

```bash
curl -fsSL https://raw.githubusercontent.com/rendis/geotap/main/install.sh | bash
```

Or build from source:

```bash
go install github.com/rendis/geotap/cmd/geotap@latest
```

## CLI Usage

### Scan by country

```bash
geotap scan -queries "restaurantes" -country Chile -output ./projects
```

### Scan by coordinates

```bash
geotap scan -queries "cafes,bars" -lat 40.4168 -lng -3.7038 -radius 5 -output ./projects
```

### Match by name + fetch photos

```bash
geotap match -lat -33.45 -lng -70.66 -name "Colegio San José" -output ./results
geotap match -lat 40.42 -lng -3.70 -name "IES Cervantes" -threshold 60 -query schools -output ./results
```

### Export to CSV

```bash
geotap export -db ./projects/geotap_20260212.db
```

### Interactive TUI

```bash
geotap
```

## Key Flags (scan)

| Flag           | Default  | Description                  |
| -------------- | -------- | ---------------------------- |
| `-queries`     | required | Comma-separated search terms |
| `-output`      | required | Output directory             |
| `-country`     |          | Country name or ISO code     |
| `-region`      |          | Region/state (optional)      |
| `-lat`/`-lng`  |          | Center coordinates           |
| `-radius`      | 10       | Search radius in km          |
| `-zoom`        | auto     | Grid level 10-16             |
| `-concurrency` | 10       | Parallel requests            |
| `-max-pages`   | 1        | Pagination depth per sector  |
| `-min-rating`  | 0        | Minimum star rating filter   |
| `-lang`        | en       | Search language              |
| `-proxy`       |          | HTTP/SOCKS5 proxy URL        |

## Key Flags (match)

| Flag           | Default  | Description                              |
| -------------- | -------- | ---------------------------------------- |
| `-lat`/`-lng`  | required | Center coordinates                       |
| `-name`        | required | Name to fuzzy-match (Jaro-Winkler)       |
| `-output`      | required | Output directory                         |
| `-query`       | schools  | Google Maps search term                  |
| `-radius`      | 0.5      | Search radius in km                      |
| `-threshold`   | 50       | Minimum similarity % (0-100)             |
| `-zoom`        | 16       | Grid level (16 = ~500m precision)        |
| `-photo-delay` | 1.5      | Seconds between photo requests           |

## Data Fields Extracted

Each business record contains: name, rating, review_count, category, categories, address, city, postal_code, country_code, lat, lng, phone, website, google_url, description, price_range, cid, place_id, open_hours, thumbnail, photos, query.

See [cli-reference.md](references/cli-reference.md) for full flag details.
See [architecture.md](references/architecture.md) for codebase structure.

## Anti-Blocking

- TLS fingerprinting via `utls` (HelloChrome_Auto)
- Chrome user agent rotation
- Exponential backoff on rate limits
- CONSENT cookie pre-set
- Optional proxy support (HTTP/SOCKS5)

## TUI Navigation

| Screen   | Keys                                                      |
| -------- | --------------------------------------------------------- |
| Home     | `n` new search, `l` load project, `r` recent, `q` quit    |
| Search   | `tab`/`shift+tab` navigate, `enter` start, `esc` back     |
| Progress | `esc` cancel (confirm twice), `ctrl+c` quit               |
| Explorer | `/` filter, `1` details, `2` json, `e` export, `esc` back |
