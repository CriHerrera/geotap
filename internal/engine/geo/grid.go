package geo

import (
	"math"

	"github.com/rendis/geotap/internal/model"
)

// ZoomToSpanDegrees converts a zoom level to the approximate span in degrees
// for a sector search. Based on the formula:
// At zoom 13, one tile covers ~0.044 degrees. For a 60px sector in a 256px tile,
// the sector covers approximately 0.044 * 60/256 ≈ 0.01 degrees.
func ZoomToSpanDegrees(zoom int) float64 {
	tileSpan := 360.0 / math.Pow(2, float64(zoom))
	return tileSpan * 60.0 / 256.0
}

// GenerateGrid creates a grid of sectors covering the given bounding box.
func GenerateGrid(minLat, minLng, maxLat, maxLng float64, zoom int) []model.Sector {
	span := ZoomToSpanDegrees(zoom)

	var sectors []model.Sector
	row := 0
	for lat := minLat + span/2; lat < maxLat; lat += span {
		col := 0
		// Adjust longitude span for Mercator distortion
		lngSpan := span / math.Cos(lat*math.Pi/180.0)
		for lng := minLng + lngSpan/2; lng < maxLng; lng += lngSpan {
			sectors = append(sectors, model.Sector{
				Lat:  lat,
				Lng:  lng,
				Span: span,
				Row:  row,
				Col:  col,
			})
			col++
		}
		row++
	}

	return sectors
}

// GenerateRadiusGrid creates a grid of sectors around a center point within a radius (km).
func GenerateRadiusGrid(centerLat, centerLng, radiusKm float64, zoom int) []model.Sector {
	// Convert radius to approximate degrees
	latDeg := radiusKm / 111.0 // ~111 km per degree latitude
	lngDeg := radiusKm / (111.0 * math.Cos(centerLat*math.Pi/180.0))

	minLat := centerLat - latDeg
	maxLat := centerLat + latDeg
	minLng := centerLng - lngDeg
	maxLng := centerLng + lngDeg

	all := GenerateGrid(minLat, minLng, maxLat, maxLng, zoom)

	// Filter sectors outside the radius
	var filtered []model.Sector
	for _, s := range all {
		if HaversineKm(centerLat, centerLng, s.Lat, s.Lng) <= radiusKm {
			filtered = append(filtered, s)
		}
	}

	return filtered
}

// HaversineKm returns the great-circle distance in km between two points.
func HaversineKm(lat1, lng1, lat2, lng2 float64) float64 {
	const earthRadiusKm = 6371.0
	dLat := (lat2 - lat1) * math.Pi / 180.0
	dLng := (lng2 - lng1) * math.Pi / 180.0
	a := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Cos(lat1*math.Pi/180.0)*math.Cos(lat2*math.Pi/180.0)*
			math.Sin(dLng/2)*math.Sin(dLng/2)
	c := 2 * math.Atan2(math.Sqrt(a), math.Sqrt(1-a))
	return earthRadiusKm * c
}
