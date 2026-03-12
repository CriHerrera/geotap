package scraper

import (
	"context"
	"fmt"
	"log"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/paulmach/orb"
	"github.com/paulmach/orb/planar"
	"github.com/rendis/geotap/internal/engine/storage"
	"github.com/rendis/geotap/internal/model"
)

type Stats struct {
	SectorsTotal     int
	SectorsDone      atomic.Int64
	BusinessesFound  atomic.Int64
	BusinessesStored atomic.Int64
	PhotosFetched    atomic.Int64
	Errors           atomic.Int64
	RateLimits       atomic.Int64
}

type Job struct {
	Sector model.Sector
	Query  string
}

// RunOptions provides optional callbacks for the scraping pipeline.
type RunOptions struct {
	// OnBusinesses is called each time a batch of businesses is found (before filtering).
	// Can be used by TUI to plot points in real-time.
	OnBusinesses func([]model.Business)
	// SuppressStderr disables the built-in stderr progress reporter.
	SuppressStderr bool
	// Stats allows passing an external Stats object for live progress tracking.
	// If nil, Run() creates its own.
	Stats *Stats
	// GeoFilter, if set, discards businesses whose coordinates fall outside the polygon.
	GeoFilter orb.MultiPolygon
}

// Run executes the scraping pipeline: for each sector*query, fetch and parse results.
func Run(ctx context.Context, sectors []model.Sector, params model.SearchParams, store *storage.Store, logger *log.Logger, opts *RunOptions) (*Stats, error) {
	if opts == nil {
		opts = &RunOptions{}
	}

	var stats *Stats
	if opts.Stats != nil {
		stats = opts.Stats
		stats.SectorsTotal = len(sectors) * len(params.Queries)
	} else {
		stats = &Stats{SectorsTotal: len(sectors) * len(params.Queries)}
	}
	client := NewClient(params.Lang, params.ProxyURL, params.Zoom)

	jobs := make(chan Job, stats.SectorsTotal)
	for _, q := range params.Queries {
		for _, s := range sectors {
			jobs <- Job{Sector: s, Query: q}
		}
	}
	close(jobs)

	var wg sync.WaitGroup
	sem := make(chan struct{}, params.Concurrency)

	// Adaptive delay: increases when rate limited
	var delayMu sync.RWMutex
	delay := time.Duration(0)

	startTime := time.Now()

	// Progress reporter
	done := make(chan struct{})
	if !opts.SuppressStderr {
		go func() {
			ticker := time.NewTicker(2 * time.Second)
			logTicker := time.NewTicker(10 * time.Second)
			defer ticker.Stop()
			defer logTicker.Stop()
			for {
				select {
				case <-ticker.C:
					elapsed := time.Since(startTime).Truncate(time.Second)
					rl := stats.RateLimits.Load()
					if rl > 0 {
						fmt.Fprintf(os.Stderr, "\r[%d/%d sectors] %d businesses | %d stored | %d errors | %d rate-limited | %s",
							stats.SectorsDone.Load(), stats.SectorsTotal,
							stats.BusinessesFound.Load(), stats.BusinessesStored.Load(),
							stats.Errors.Load(), rl, elapsed)
					} else {
						fmt.Fprintf(os.Stderr, "\r[%d/%d sectors] %d businesses | %d stored | %d errors | %s",
							stats.SectorsDone.Load(), stats.SectorsTotal,
							stats.BusinessesFound.Load(), stats.BusinessesStored.Load(),
							stats.Errors.Load(), elapsed)
					}
				case <-logTicker.C:
					elapsed := time.Since(startTime).Truncate(time.Second)
					logger.Printf("PROGRESS sectors=%d/%d found=%d stored=%d errors=%d rate_limits=%d elapsed=%s",
						stats.SectorsDone.Load(), stats.SectorsTotal,
						stats.BusinessesFound.Load(), stats.BusinessesStored.Load(),
						stats.Errors.Load(), stats.RateLimits.Load(), elapsed)
				case <-done:
					return
				}
			}
		}()
	} else {
		go func() {
			logTicker := time.NewTicker(10 * time.Second)
			defer logTicker.Stop()
			for {
				select {
				case <-logTicker.C:
					elapsed := time.Since(startTime).Truncate(time.Second)
					logger.Printf("PROGRESS sectors=%d/%d found=%d stored=%d errors=%d rate_limits=%d elapsed=%s",
						stats.SectorsDone.Load(), stats.SectorsTotal,
						stats.BusinessesFound.Load(), stats.BusinessesStored.Load(),
						stats.Errors.Load(), stats.RateLimits.Load(), elapsed)
				case <-done:
					return
				}
			}
		}()
	}

	// consecutiveRL tracks consecutive rate limits to detect persistent blocking
	var consecutiveRL atomic.Int64

	for job := range jobs {
		select {
		case <-ctx.Done():
			close(done)
			return stats, ctx.Err()
		default:
		}

		// Early abort: if we've been rate limited 50+ times in a row, Google has blocked us
		if consecutiveRL.Load() > 50 {
			logger.Printf("ABORT: persistent rate limiting (50+ consecutive), stopping")
			if !opts.SuppressStderr {
				fmt.Fprintf(os.Stderr, "\n[!] Persistent rate limiting detected — aborting. Try again later or reduce concurrency/zoom.\n")
			}
			break
		}

		sem <- struct{}{}
		wg.Add(1)
		go func(j Job) {
			defer wg.Done()
			defer func() { <-sem }()

			// Apply adaptive delay
			delayMu.RLock()
			d := delay
			delayMu.RUnlock()
			if d > 0 {
				time.Sleep(d)
			}

			processJob(ctx, client, store, j, params, stats, logger, opts, func(rateLimited bool) {
				if rateLimited {
					consecutiveRL.Add(1)
					delayMu.Lock()
					if delay < 5*time.Second {
						delay += 500 * time.Millisecond
					}
					delayMu.Unlock()
				} else {
					consecutiveRL.Store(0)
					delayMu.Lock()
					if delay > 0 {
						delay -= 100 * time.Millisecond
						if delay < 0 {
							delay = 0
						}
					}
					delayMu.Unlock()
				}
			})
		}(job)
	}

	wg.Wait()
	close(done)

	// Final progress line
	if !opts.SuppressStderr {
		elapsed := time.Since(startTime).Truncate(time.Second)
		fmt.Fprintf(os.Stderr, "\r[%d/%d sectors] %d businesses | %d stored | %d errors | %s\n",
			stats.SectorsDone.Load(), stats.SectorsTotal,
			stats.BusinessesFound.Load(), stats.BusinessesStored.Load(),
			stats.Errors.Load(), elapsed)
	}

	return stats, nil
}

func processJob(ctx context.Context, client *Client, store *storage.Store, job Job, params model.SearchParams, stats *Stats, logger *log.Logger, opts *RunOptions, adjustDelay func(rateLimited bool)) {
	defer stats.SectorsDone.Add(1)

	maxPages := params.MaxPages
	if maxPages <= 0 {
		maxPages = 1
	}

	for page := 0; page < maxPages; page++ {
		select {
		case <-ctx.Done():
			return
		default:
		}

		offset := page * pageSize
		body, err := client.SearchMap(job.Sector, job.Query, offset)
		if err != nil {
			if rl, ok := err.(*RateLimitError); ok {
				stats.RateLimits.Add(1)
				adjustDelay(true)
				logger.Printf("RATE_LIMIT sector=%d,%d status=%d query=%q", job.Sector.Row, job.Sector.Col, rl.StatusCode, job.Query)
			} else {
				logger.Printf("ERROR sector=%d,%d page=%d err=%v", job.Sector.Row, job.Sector.Col, page, err)
			}
			stats.Errors.Add(1)
			return
		}

		adjustDelay(false)

		if params.Debug {
			debugFile := fmt.Sprintf("debug_sector_%d_%d_page_%d.json", job.Sector.Row, job.Sector.Col, page)
			os.WriteFile(debugFile, body, 0644)
		}

		businesses, hasMore := ParseMapResponse(body, job.Query)
		stats.BusinessesFound.Add(int64(len(businesses)))

		// Apply rating filter
		if params.MinRating > 0 || params.MaxRating > 0 {
			businesses = filterByRating(businesses, params.MinRating, params.MaxRating)
		}

		// Apply geographic filter
		if len(opts.GeoFilter) > 0 {
			businesses = filterByGeo(businesses, opts.GeoFilter)
		}

		// Fetch photos for each business if enabled
		if params.FetchPhotos {
			for i := range businesses {
				select {
				case <-ctx.Done():
					return
				default:
				}
				if businesses[i].CID == "" {
					continue
				}
				photoBody, err := client.FetchPlacePhotos(businesses[i].CID)
				if err != nil {
					logger.Printf("PHOTO_ERROR cid=%s err=%v", businesses[i].CID, err)
					continue
				}
				if photos := ParsePlacePhotos(photoBody); photos != "" {
					businesses[i].Photos = photos
					stats.PhotosFetched.Add(1)
				}
				time.Sleep(200 * time.Millisecond)
			}
		}

		// Notify callback with filtered results
		if opts.OnBusinesses != nil && len(businesses) > 0 {
			opts.OnBusinesses(businesses)
		}

		if len(businesses) > 0 {
			inserted, err := store.InsertBatch(businesses)
			if err != nil {
				stats.Errors.Add(1)
			} else {
				stats.BusinessesStored.Add(int64(inserted))
			}
		}

		if !hasMore {
			break
		}
	}
}

func filterByRating(businesses []model.Business, minRating, maxRating float64) []model.Business {
	var filtered []model.Business
	for _, b := range businesses {
		if minRating > 0 && b.Rating < minRating {
			continue
		}
		if maxRating > 0 && b.Rating > maxRating {
			continue
		}
		filtered = append(filtered, b)
	}
	return filtered
}

func filterByGeo(businesses []model.Business, poly orb.MultiPolygon) []model.Business {
	var filtered []model.Business
	for _, b := range businesses {
		if b.Lat == 0 && b.Lng == 0 {
			continue
		}
		if planar.MultiPolygonContains(poly, orb.Point{b.Lng, b.Lat}) {
			filtered = append(filtered, b)
		}
	}
	return filtered
}
