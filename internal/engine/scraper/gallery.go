package scraper

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
)

// FetchGalleryPhotos uses headless Chrome to extract all photo URLs from
// a Google Maps place gallery. It navigates to the place page, opens the
// gallery, clicks through all tabs (Todas, Edificio, Street View y 360°),
// scrolls thumbnails, and uses arrow navigation to capture every photo.
// If debugDir is non-empty, screenshots are saved there for troubleshooting.
func FetchGalleryPhotos(ctx context.Context, placeID string, maxPhotos int, debugDir string, logger *log.Logger) ([]string, error) {
	if maxPhotos <= 0 {
		maxPhotos = 100
	}

	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.Flag("headless", true),
		chromedp.Flag("disable-gpu", true),
		chromedp.Flag("no-sandbox", true),
		chromedp.Flag("disable-dev-shm-usage", true),
		chromedp.Flag("disable-extensions", true),
		chromedp.UserAgent("Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"),
		chromedp.WindowSize(1280, 900),
	)

	allocCtx, allocCancel := chromedp.NewExecAllocator(ctx, opts...)
	defer allocCancel()

	chromeCtx, chromeCancel := chromedp.NewContext(allocCtx)
	defer chromeCancel()

	chromeCtx, timeoutCancel := context.WithTimeout(chromeCtx, 5*time.Minute)
	defer timeoutCancel()

	// Collect image URLs from network requests (thread-safe)
	var mu sync.Mutex
	photoURLs := make(map[string]bool)  // regular photo base URLs
	panoids := make(map[string]bool)     // Street View panorama IDs
	addURL := func(u string) {
		if !isGalleryPhotoURL(u) {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		if isStreetViewURL(u) {
			if pid := extractPanoid(u); pid != "" {
				panoids[pid] = true
			}
		} else {
			base := stripSizeParam(u)
			photoURLs[base] = true
		}
	}
	countURLs := func() int {
		mu.Lock()
		defer mu.Unlock()
		return len(photoURLs) + len(panoids)
	}

	chromedp.ListenTarget(chromeCtx, func(ev interface{}) {
		if req, ok := ev.(*network.EventRequestWillBeSent); ok {
			addURL(req.Request.URL)
		}
	})

	logf := func(format string, args ...interface{}) {
		if logger != nil {
			logger.Printf(format, args...)
		}
	}

	screenshotIdx := 0
	saveScreenshot := func(name string) {
		if debugDir == "" {
			return
		}
		screenshotIdx++
		var buf []byte
		_ = chromedp.Run(chromeCtx, chromedp.ActionFunc(func(ctx context.Context) error {
			var err error
			buf, err = page.CaptureScreenshot().WithFormat(page.CaptureScreenshotFormatJpeg).WithQuality(80).Do(ctx)
			return err
		}))
		if len(buf) > 0 {
			os.MkdirAll(debugDir, 0755)
			os.WriteFile(filepath.Join(debugDir, fmt.Sprintf("%02d_%s.jpg", screenshotIdx, name)), buf, 0644)
		}
	}

	placeURL := "https://www.google.com/maps/place/?q=place_id:" + placeID + "&hl=es"
	logf("GALLERY navigating to %s", placeURL)

	// Step 1: Navigate to place page
	if err := chromedp.Run(chromeCtx,
		network.Enable(),
		chromedp.Navigate(placeURL),
		chromedp.Sleep(4*time.Second),
	); err != nil {
		return nil, fmt.Errorf("navigating to place: %w", err)
	}
	saveScreenshot("page_loaded")

	// Step 2: Dismiss consent dialogs
	_ = chromedp.Run(chromeCtx,
		chromedp.Evaluate(`
			for (let btn of document.querySelectorAll('button')) {
				let text = btn.textContent.toLowerCase();
				if (text.includes('aceptar todo') || text.includes('accept all') || text.includes('rechazar') || text.includes('reject')) {
					btn.click();
					break;
				}
			}
			let form = document.querySelector('form[action*="consent"]');
			if (form) {
				let submit = form.querySelector('button, input[type="submit"]');
				if (submit) submit.click();
			}
		`, nil),
		chromedp.Sleep(2*time.Second),
	)

	// Step 3: Open gallery by clicking on the photo area
	var clickResult string
	_ = chromedp.Run(chromeCtx,
		chromedp.Evaluate(`
			(function() {
				// Strategy 1: Click button with photo-related aria-label
				let photoBtn = document.querySelector('button[aria-label*="oto"]');
				if (photoBtn && photoBtn.offsetHeight > 0) {
					photoBtn.click();
					return 'clicked photo button: ' + photoBtn.ariaLabel;
				}
				// Strategy 2: Click the large header image container
				let headerImg = document.querySelector('img[src*="googleusercontent"]');
				if (headerImg) {
					let parent = headerImg.closest('button, a, [role="link"], [role="button"], [jsaction]');
					if (parent) { parent.click(); return 'clicked header image parent'; }
					headerImg.click();
					return 'clicked header image directly';
				}
				// Strategy 3: Background-image div
				for (let div of document.querySelectorAll('div[style*="background-image"]')) {
					if (div.style.backgroundImage.includes('googleusercontent') && div.offsetHeight > 50) {
						let parent = div.closest('button, a, [jsaction]');
						if (parent) { parent.click(); return 'clicked bg-image parent'; }
						div.click();
						return 'clicked bg-image div';
					}
				}
				return 'no photo element found';
			})()
		`, &clickResult),
		chromedp.Sleep(3*time.Second),
	)
	logf("GALLERY open gallery: %s", clickResult)
	saveScreenshot("gallery_opened")

	logf("GALLERY photos from initial load: %d", countURLs())

	// Step 4: Discover gallery tabs (Todas, Edificio, Street View y 360°, etc.)
	var tabLabels []string
	_ = chromedp.Run(chromeCtx,
		chromedp.Evaluate(`
			(function() {
				let tabs = [];
				// Gallery tabs are typically <button> elements in a tab bar
				// Look for role="tab" or tab-like buttons near the top of the gallery panel
				let candidates = document.querySelectorAll('button[role="tab"], [role="tablist"] button');
				if (candidates.length === 0) {
					// Fallback: look for buttons with text like "Todas", "Edificio", "Street View"
					document.querySelectorAll('button').forEach(btn => {
						let text = btn.textContent.trim();
						if (btn.offsetHeight > 0 && btn.offsetWidth > 0) {
							let lower = text.toLowerCase();
							if (lower === 'todas' || lower === 'all' ||
								lower.includes('edificio') || lower.includes('exterior') ||
								lower.includes('street view') || lower.includes('360') ||
								lower.includes('interior') || lower.includes('vídeo') || lower.includes('video') ||
								lower.includes('menú') || lower.includes('menu') ||
								lower.includes('by owner') || lower.includes('del propietario')) {
								candidates = [...candidates, btn];
							}
						}
					});
				}
				for (let btn of candidates) {
					let label = btn.textContent.trim();
					if (label && !tabs.includes(label)) {
						tabs.push(label);
					}
				}
				return tabs;
			})()
		`, &tabLabels),
	)
	logf("GALLERY found %d tabs: %v", len(tabLabels), tabLabels)

	// Helper: collect photos from the current view by scrolling thumbnails and using arrows
	collectFromCurrentView := func(viewName string) {
		logf("GALLERY collecting from view: %s (current count: %d)", viewName, countURLs())

		// Find and tag the scrollable thumbnail panel once, then scroll it repeatedly
		_ = chromedp.Run(chromeCtx,
			chromedp.Evaluate(`
				(function() {
					// Find the best scrollable container with thumbnails
					let best = null;
					let bestScore = 0;
					document.querySelectorAll('div, section').forEach(el => {
						if (el.scrollHeight <= el.clientHeight + 20) return;
						if (el.offsetWidth < 50 || el.offsetHeight < 50) return;
						let links = el.querySelectorAll('a, img[src*="googleusercontent"], img[src*="ggpht"]');
						let bgImgs = el.querySelectorAll('[style*="background-image"]');
						let score = links.length + bgImgs.length;
						if (score > bestScore) {
							bestScore = score;
							best = el;
						}
					});
					if (best) {
						best.id = '__geotap_scroll_panel__';
						return 'tagged panel score=' + bestScore + ' scrollHeight=' + best.scrollHeight;
					}
					return 'no panel found';
				})()
			`, nil),
		)

		// Scroll the tagged panel incrementally
		for scroll := 0; scroll < 20; scroll++ {
			if countURLs() >= maxPhotos {
				break
			}

			var scrollInfo string
			_ = chromedp.Run(chromeCtx,
				chromedp.Evaluate(fmt.Sprintf(`
					(function() {
						let panel = document.getElementById('__geotap_scroll_panel__');
						if (!panel) return 'panel not found';
						let before = panel.scrollTop;
						let max = panel.scrollHeight - panel.clientHeight;
						panel.scrollTop = Math.min(before + 400, max);
						let after = panel.scrollTop;
						return 'scroll ' + %d + ': ' + Math.round(before) + ' → ' + Math.round(after) + '/' + panel.scrollHeight + ' (max=' + Math.round(max) + ')';
					})()
				`, scroll), &scrollInfo),
				chromedp.Sleep(1200*time.Millisecond),
			)

			if scroll == 0 || scroll%5 == 0 || scroll == 19 {
				logf("GALLERY %s (photos: %d)", scrollInfo, countURLs())
			}
		}

		// Extract all photo URLs from current DOM (img src + background-image + anchor hrefs)
		var domURLs []string
		_ = chromedp.Run(chromeCtx,
			chromedp.Evaluate(`
				(function() {
					let urls = new Set();
					// img src
					document.querySelectorAll('img').forEach(img => {
						if (img.src && (img.src.includes('googleusercontent.com') || img.src.includes('ggpht.com'))) {
							urls.add(img.src);
						}
					});
					// background-image
					document.querySelectorAll('[style*="background-image"]').forEach(el => {
						let m = el.style.backgroundImage.match(/url\("?([^")\s]+)"?\)/);
						if (m && (m[1].includes('googleusercontent.com') || m[1].includes('ggpht.com'))) {
							urls.add(m[1]);
						}
					});
					// anchor hrefs that point to photos
					document.querySelectorAll('a[href]').forEach(a => {
						if (a.href.includes('googleusercontent.com') || a.href.includes('ggpht.com')) {
							urls.add(a.href);
						}
					});
					return [...urls];
				})()
			`, &domURLs),
		)
		for _, u := range domURLs {
			addURL(u)
		}
		logf("GALLERY DOM extraction for %s: %d URLs from DOM, total unique: %d", viewName, len(domURLs), countURLs())

		// Use arrow navigation (> button) to cycle through photos in the main viewer
		// This loads full-resolution versions and captures new network requests
		prevCount := countURLs()
		stale := 0
		for arrow := 0; arrow < 80; arrow++ {
			if countURLs() >= maxPhotos {
				break
			}
			if stale >= 8 {
				break
			}

			var arrowResult string
			_ = chromedp.Run(chromeCtx,
				chromedp.Evaluate(`
					(function() {
						// Find the forward/next arrow button
						// It's typically a button with an SVG arrow, positioned on the right side
						let buttons = document.querySelectorAll('button');
						let bestBtn = null;
						let bestX = 0;
						for (let btn of buttons) {
							if (btn.offsetHeight === 0 || btn.offsetWidth === 0) continue;
							let rect = btn.getBoundingClientRect();
							// Arrow buttons are small, on the right side of the photo viewer
							if (rect.width < 100 && rect.height < 100 && rect.width > 20 && rect.height > 20) {
								// Check if it has an arrow-like child (svg, img, or specific aria-label)
								let label = (btn.ariaLabel || '').toLowerCase();
								let hasSvg = btn.querySelector('svg, img') !== null;
								if (label.includes('next') || label.includes('siguiente') || label.includes('adelante') || label.includes('forward')) {
									btn.click();
									return 'clicked next button by label: ' + btn.ariaLabel;
								}
								// Right-side button with svg
								if (hasSvg && rect.left > window.innerWidth * 0.4) {
									if (rect.left > bestX) {
										bestX = rect.left;
										bestBtn = btn;
									}
								}
							}
						}
						if (bestBtn) {
							bestBtn.click();
							return 'clicked rightmost arrow at x=' + Math.round(bestX);
						}
						// Try keyboard: press right arrow
						document.dispatchEvent(new KeyboardEvent('keydown', {key: 'ArrowRight', code: 'ArrowRight', keyCode: 39, bubbles: true}));
						return 'sent ArrowRight keydown';
					})()
				`, &arrowResult),
				chromedp.Sleep(800*time.Millisecond),
			)

			cur := countURLs()
			if cur > prevCount {
				stale = 0
			} else {
				stale++
			}
			prevCount = cur

			if arrow%10 == 0 {
				logf("GALLERY arrow %d: %s (photos: %d, stale: %d)", arrow, arrowResult, cur, stale)
			}
		}

		saveScreenshot("view_" + sanitizeScreenshotName(viewName))
	}

	// Step 5: Process each tab
	if len(tabLabels) == 0 {
		// No tabs found, just collect from current view
		collectFromCurrentView("default")
	} else {
		for tabIdx, tabLabel := range tabLabels {
			if countURLs() >= maxPhotos {
				break
			}

			logf("GALLERY clicking tab %d: %q", tabIdx, tabLabel)
			var tabResult string
			_ = chromedp.Run(chromeCtx,
				chromedp.Evaluate(fmt.Sprintf(`
					(function() {
						let target = %q;
						// Try role="tab" buttons first
						let tabs = document.querySelectorAll('button[role="tab"], [role="tablist"] button');
						for (let tab of tabs) {
							if (tab.textContent.trim() === target) {
								tab.click();
								return 'clicked tab: ' + target;
							}
						}
						// Fallback: any button matching text
						for (let btn of document.querySelectorAll('button')) {
							if (btn.textContent.trim() === target && btn.offsetHeight > 0) {
								btn.click();
								return 'clicked button: ' + target;
							}
						}
						return 'tab not found: ' + target;
					})()
				`, tabLabel), &tabResult),
				chromedp.Sleep(2*time.Second),
			)
			logf("GALLERY tab click: %s", tabResult)
			saveScreenshot("tab_" + sanitizeScreenshotName(tabLabel))

			collectFromCurrentView(tabLabel)
		}
	}

	// Step 6: Also try clicking individual thumbnails in the left panel that we haven't triggered yet
	logf("GALLERY clicking individual thumbnails... (current: %d)", countURLs())
	var thumbClickCount int
	_ = chromedp.Run(chromeCtx,
		chromedp.Evaluate(`
			(function() {
				// Click each visible thumbnail/link to trigger loading
				let items = document.querySelectorAll('a[href*="/maps/place/"]');
				let clicked = 0;
				for (let i = 0; i < Math.min(items.length, 100); i++) {
					let item = items[i];
					if (item.offsetHeight > 0) {
						item.click();
						clicked++;
					}
				}
				return clicked;
			})()
		`, &thumbClickCount),
		chromedp.Sleep(2*time.Second),
	)
	logf("GALLERY clicked %d thumbnail links", thumbClickCount)

	// Final DOM extraction
	var finalURLs []string
	_ = chromedp.Run(chromeCtx,
		chromedp.Evaluate(`
			(function() {
				let urls = new Set();
				document.querySelectorAll('img').forEach(img => {
					if (img.src && (img.src.includes('googleusercontent.com') || img.src.includes('ggpht.com'))) {
						urls.add(img.src);
					}
				});
				document.querySelectorAll('[style*="background-image"]').forEach(el => {
					let m = el.style.backgroundImage.match(/url\("?([^")\s]+)"?\)/);
					if (m && (m[1].includes('googleusercontent.com') || m[1].includes('ggpht.com'))) {
						urls.add(m[1]);
					}
				});
				return [...urls];
			})()
		`, &finalURLs),
	)
	for _, u := range finalURLs {
		addURL(u)
	}

	saveScreenshot("final")
	logf("GALLERY total unique: %d regular photos + %d streetview panoids", len(photoURLs), len(panoids))

	// Build result: regular photos first, then Street View panoramas
	mu.Lock()
	var result []string
	for u := range photoURLs {
		result = append(result, u+"=s1600")
		if len(result) >= maxPhotos {
			break
		}
	}
	for pid := range panoids {
		if len(result) >= maxPhotos {
			break
		}
		result = append(result, streetViewPanoURL(pid))
	}
	mu.Unlock()

	logf("GALLERY extracted %d photos (%d regular + %d pano) for place_id=%s",
		len(result), len(photoURLs), len(panoids), placeID)
	return result, nil
}

// isGalleryPhotoURL checks if a URL is a Google Maps place photo (regular or Street View).
func isGalleryPhotoURL(u string) bool {
	// Must be hosted on googleusercontent.com or ggpht.com (not just referenced in a query param)
	isGoogleUserContent := strings.HasPrefix(u, "https://lh3.googleusercontent.com/") ||
		strings.HasPrefix(u, "https://lh4.googleusercontent.com/") ||
		strings.HasPrefix(u, "https://lh5.googleusercontent.com/") ||
		strings.HasPrefix(u, "https://lh6.googleusercontent.com/") ||
		strings.HasPrefix(u, "https://streetviewpixels-pa.googleapis.com/") ||
		strings.Contains(u, ".ggpht.com/")

	if !isGoogleUserContent {
		return false
	}

	// Skip profile photos and avatars
	if strings.Contains(u, "/a-/") {
		return false
	}
	// Skip tiny icons (size <=64px)
	for _, s := range []string{"=s16-", "=s24-", "=s32-", "=s36-", "=s40-", "=s44-", "=s48-", "=s64-"} {
		if strings.Contains(u, s) {
			return false
		}
	}
	// Skip default profile/avatar images
	if strings.Contains(u, "-mo/") || strings.Contains(u, "-mo-") {
		return false
	}
	return true
}

// isStreetViewURL checks if a URL is a Street View tile or thumbnail.
func isStreetViewURL(u string) bool {
	return strings.HasPrefix(u, "https://streetviewpixels-pa.googleapis.com/")
}

// extractPanoid extracts the panoid parameter from a Street View URL.
func extractPanoid(u string) string {
	const marker = "panoid="
	idx := strings.Index(u, marker)
	if idx < 0 {
		return ""
	}
	start := idx + len(marker)
	end := strings.IndexAny(u[start:], "&?# ")
	if end < 0 {
		return u[start:]
	}
	return u[start : start+end]
}

// streetViewPanoURL builds a high-resolution Street View thumbnail URL from a panoid.
func streetViewPanoURL(panoid string) string {
	return fmt.Sprintf(
		"https://streetviewpixels-pa.googleapis.com/v1/thumbnail?panoid=%s&cb_client=maps_sv.tactile.gps&w=1600&h=800&yaw=0&pitch=0&thumbfov=100",
		panoid,
	)
}

// stripSizeParam removes the =sNNN or =wNNN-hNNN size suffix.
func stripSizeParam(u string) string {
	if idx := strings.LastIndex(u, "="); idx > 0 {
		return u[:idx]
	}
	return u
}

// sanitizeScreenshotName makes a string safe for use as a filename.
func sanitizeScreenshotName(s string) string {
	s = strings.ToLower(s)
	s = strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			return r
		}
		return '_'
	}, s)
	if len(s) > 30 {
		s = s[:30]
	}
	return s
}
