package match

import (
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// Similarity returns the Jaro-Winkler similarity (0.0–1.0) between two strings.
// Both strings are normalized: lowercased, accents stripped, extra whitespace collapsed.
func Similarity(a, b string) float64 {
	a = normalize(a)
	b = normalize(b)
	if a == b {
		return 1.0
	}
	return jaroWinkler(a, b)
}

// normalize lowercases, removes accents (NFD decomposition), and collapses whitespace.
func normalize(s string) string {
	s = strings.ToLower(s)
	// NFD decompose then strip combining marks (accents)
	var buf strings.Builder
	for _, r := range norm.NFD.String(s) {
		if !unicode.Is(unicode.Mn, r) { // Mn = nonspacing mark (accents)
			buf.WriteRune(r)
		}
	}
	return strings.Join(strings.Fields(buf.String()), " ")
}

// jaro computes the Jaro similarity between two strings.
func jaro(s1, s2 string) float64 {
	if len(s1) == 0 && len(s2) == 0 {
		return 1.0
	}
	if len(s1) == 0 || len(s2) == 0 {
		return 0.0
	}

	matchDist := max(len(s1), len(s2))/2 - 1
	if matchDist < 0 {
		matchDist = 0
	}

	s1Matches := make([]bool, len(s1))
	s2Matches := make([]bool, len(s2))

	matches := 0
	transpositions := 0

	r1 := []rune(s1)
	r2 := []rune(s2)

	for i := range r1 {
		lo := max(0, i-matchDist)
		hi := min(len(r2)-1, i+matchDist)
		for j := lo; j <= hi; j++ {
			if s2Matches[j] || r1[i] != r2[j] {
				continue
			}
			s1Matches[i] = true
			s2Matches[j] = true
			matches++
			break
		}
	}

	if matches == 0 {
		return 0.0
	}

	k := 0
	for i := range r1 {
		if !s1Matches[i] {
			continue
		}
		for !s2Matches[k] {
			k++
		}
		if r1[i] != r2[k] {
			transpositions++
		}
		k++
	}

	m := float64(matches)
	return (m/float64(len(r1)) + m/float64(len(r2)) + (m-float64(transpositions)/2)/m) / 3.0
}

// jaroWinkler applies the Winkler prefix bonus to the Jaro similarity.
func jaroWinkler(s1, s2 string) float64 {
	j := jaro(s1, s2)

	// Common prefix up to 4 characters
	r1 := []rune(s1)
	r2 := []rune(s2)
	prefixLen := 0
	for i := 0; i < min(len(r1), min(len(r2), 4)); i++ {
		if r1[i] != r2[i] {
			break
		}
		prefixLen++
	}

	return j + float64(prefixLen)*0.1*(1-j)
}
