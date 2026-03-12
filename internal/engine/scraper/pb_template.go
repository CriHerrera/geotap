package scraper

import (
	"fmt"
	"math"
)

const (
	viewportW = 1024
	viewportH = 768
	pageSize  = 20
)

// BuildPB constructs the pb= protobuf URL parameter for tbm=map requests.
// The template is derived from gosom/google-maps-scraper's proven format.
func BuildPB(lat, lng float64, zoom, offset int) string {
	alt := altitude(lat, zoom)
	return fmt.Sprintf(
		"!4m12!1m3!1d%.4f!2d%.7f!3d%.7f!2m3!1f0!2f0!3f0!3m2!1i%d!2i%d!4f13.1"+
			"!7i%d!8i%d!10b1"+
			"!12m22!1m3!18b1!30b1!34e1!2m3!5m1!6e2!20e3!4b0!10b1!12b1!13b1!16b1!17m1!3e1!20m3!5e2!6b1!14b1!46m1!1b0!96b1"+
			"!19m4!2m3!1i360!2i120!4i8",
		alt, lng, lat,
		viewportW, viewportH,
		pageSize, offset,
	)
}

// BuildPlacePB constructs the pb= parameter for maps/preview/place requests.
// The CID is the hex-encoded place identifier from the tbm=map response (e.g. "0x...:0x...").
func BuildPlacePB(cid string) string {
	return fmt.Sprintf(
		"!1m14!1s%s"+
			"!3m12!1m3!1d5000!2d0!3d0!2m3!1f0!2f0!3f0!3m2!1i%d!2i%d!4f13.1"+
			"!12m4!2m3!1i360!2i120!4i8"+
			"!13m57!2m2!1i203!2i100!3m2!2i4!5b1"+
			"!6m6!1m2!1i86!2i86!1m2!1i408!2i240"+
			"!7m33!1m3!1e1!2b0!3e3!1m3!1e2!2b1!3e2!1m3!1e2!2b0!3e3"+
			"!1m3!1e8!2b0!3e3!1m3!1e10!2b0!3e3!1m3!1e10!2b1!3e2"+
			"!1m3!1e10!2b0!3e4!1m3!1e9!2b1!3e2!2b1!9b0"+
			"!15m8!1m7!1m2!1m1!1e2!2m2!1i195!2i195!3i20"+
			"!15m108!1m26!13m9!2b1!3b1!4b1!6i1!8b1!9b1!14b1!20b1!25b1"+
			"!18m15!3b1!4b1!5b1!6b1!13b1!14b1!17b1!21b1!22b1!30b1!32b1!33m1!1b1!34b1!36e2"+
			"!10m1!8e3!11m1!3e1!17b1!20m2!1e3!1e6!24b1!25b1!26b1!27b1!29b1!30m1!2b1"+
			"!36b1!37b1!39m3!2m2!2i1!3i1!43b1!52b1!54m1!1b1!55b1!56m1!1b1"+
			"!61m2!1m1!1e1!65m5!3m4!1m3!1m2!1i224!2i298"+
			"!72m22!1m8!2b1!5b1!7b1!12m4!1b1!2b1!4m1!1e1!4b1"+
			"!8m10!1m6!4m1!1e1!4m1!1e3!4m1!1e4"+
			"!3sother_user_google_review_posts__and__hotel_and_vr_partner_review_posts"+
			"!6m1!1e1!9b1!89b1!90m2!1m1!1e2!98m3!1b1!2b1!3b1"+
			"!103b1!113b1!114m3!1b1!2m1!1b1!117b1!122m1!1b1!126b1!127b1!128m1!1b0"+
			"!21m0!22m1!1e81"+
			"!30m8!3b1!6m2!1b1!2b1!7m2!1e3!2b1!9b1"+
			"!34m5!7b1!10b1!14b1!15m1!1b0!37i770",
		cid, viewportW, viewportH,
	)
}

// altitude converts zoom level to meters for the !1d field.
// Formula: alt = (2 * pi * R * viewportH) / (512 * 2^zoom)
func altitude(lat float64, zoom int) float64 {
	const earthRadius = 6371010.0
	latRad := lat * math.Pi / 180
	return (2 * math.Pi * earthRadius * float64(viewportH) * math.Cos(latRad)) / (512 * math.Pow(2, float64(zoom)))
}
