# CLI Reference

## Commands

| Command | Description |
|---------|-------------|
| `geotap` | Launch interactive TUI |
| `geotap scan [flags]` | Run headless scan |
| `geotap match [flags]` | Fuzzy match by name + fetch photos |
| `geotap export [flags]` | Export .db to CSV |
| `geotap version` | Show version |

## Scan Flags

| Flag | Type | Default | Required | Description |
|------|------|---------|----------|-------------|
| `-queries` | string | | yes | Comma-separated search terms |
| `-output` | string | | yes | Output directory for .db and .log |
| `-country` | string | | yes* | Country name or ISO code (2 or 3 letter) |
| `-region` | string | | no | Region/state within country |
| `-province` | string | | no | Province (optional) |
| `-city` | string | | no | City (optional) |
| `-lat` | float | 0 | yes* | Center latitude |
| `-lng` | float | 0 | yes* | Center longitude |
| `-radius` | float | 10 | no | Search radius in km |
| `-zoom` | int | auto | no | Grid zoom 10-16 (10=country, 13=radius) |
| `-concurrency` | int | 10 | no | Max concurrent requests |
| `-max-pages` | int | 1 | no | Pagination pages per sector |
| `-min-rating` | float | 0 | no | Minimum star rating filter |
| `-max-rating` | float | 0 | no | Maximum star rating filter |
| `-lang` | string | en | no | Search language code |
| `-proxy` | string | | no | HTTP/SOCKS5 proxy URL |
| `-debug` | bool | false | no | Dump raw responses |

\* Either `-country` or `-lat`/`-lng` is required.

## Match Flags

| Flag | Type | Default | Required | Description |
|------|------|---------|----------|-------------|
| `-lat` | float | 0 | yes | Center latitude |
| `-lng` | float | 0 | yes | Center longitude |
| `-name` | string | | yes | Name to fuzzy-match (Jaro-Winkler) |
| `-output` | string | | yes | Output directory |
| `-query` | string | schools | no | Google Maps search term |
| `-radius` | float | 0.5 | no | Search radius in km |
| `-threshold` | float | 50 | no | Minimum similarity % (0-100) |
| `-zoom` | int | 16 | no | Grid zoom level |
| `-concurrency` | int | 5 | no | Max concurrent requests |
| `-max-pages` | int | 1 | no | Pagination pages per sector |
| `-photo-delay` | float | 1.5 | no | Seconds between photo requests |
| `-lang` | string | en | no | Search language code |
| `-proxy` | string | | no | HTTP/SOCKS5 proxy URL |
| `-debug` | bool | false | no | Dump raw responses |

## Export Flags

| Flag | Type | Default | Required | Description |
|------|------|---------|----------|-------------|
| `-db` | string | | yes | Path to .db file |
| `-output` | string | auto | no | Output CSV path |
| `-format` | string | csv | no | Export format |

## Examples

Country-wide scan:
```bash
geotap scan -queries "restaurantes,cafes" -country Spain -output ./data
```

Region scan:
```bash
geotap scan -queries "hotels" -country Chile -region "Santiago" -output ./data
```

Coordinate scan with filters:
```bash
geotap scan \
  -queries "restaurants" \
  -lat 40.4168 -lng -3.7038 \
  -radius 5 \
  -min-rating 4.0 \
  -concurrency 50 \
  -output ./data
```

High-density scan:
```bash
geotap scan \
  -queries "pharmacies" \
  -country Germany \
  -zoom 13 \
  -concurrency 30 \
  -output ./data
```

Match by name and fetch photos:
```bash
geotap match -lat -33.45 -lng -70.66 -name "Colegio San José" -output ./results
geotap match -lat 40.42 -lng -3.70 -name "IES Cervantes" -threshold 60 -query schools -output ./results
```

Export results:
```bash
geotap export -db ./data/geotap_20260212_120000.db -output results.csv
```

## Output Files

Each scan generates timestamped files in the output directory:

| File | Description |
|------|-------------|
| `geotap_YYYYMMDD_HHMMSS.db` | SQLite database with business records |
| `geotap_YYYYMMDD_HHMMSS.log` | Session log with timestamps and stats |
