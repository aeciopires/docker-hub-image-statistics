package server

import (
	"cmp"
	"fmt"
	"slices"

	"github.com/aeciopires/docker-hub-image-statistics/internal/stats"
)

// sortImages orders images in place by the requested key (descending for
// numbers and dates, ascending for names). Ties are broken by key.
func sortImages(images []stats.ImageStats, by string) error {
	var cmpFn func(a, b stats.ImageStats) int
	switch by {
	case "", "pulls":
		cmpFn = func(a, b stats.ImageStats) int { return cmp.Compare(b.PullsTotal, a.PullsTotal) }
	case "1d", "7d", "30d":
		cmpFn = func(a, b stats.ImageStats) int { return cmp.Compare(b.Pulls[by].Pulls, a.Pulls[by].Pulls) }
	case "last":
		cmpFn = func(a, b stats.ImageStats) int { return cmp.Compare(b.LastCollection.Pulls, a.LastCollection.Pulls) }
	case "stars":
		cmpFn = func(a, b stats.ImageStats) int { return cmp.Compare(b.Stars, a.Stars) }
	case "updated":
		cmpFn = func(a, b stats.ImageStats) int { return b.LastUpdated.Compare(a.LastUpdated) }
	case "name":
		cmpFn = func(stats.ImageStats, stats.ImageStats) int { return 0 }
	default:
		return fmt.Errorf("invalid sort %q (pulls, last, 1d, 7d, 30d, stars, updated, name)", by)
	}
	slices.SortStableFunc(images, func(a, b stats.ImageStats) int {
		return cmp.Or(cmpFn(a, b), cmp.Compare(a.Key(), b.Key()))
	})
	return nil
}
