package stats

import (
	"context"
	"errors"
	"time"

	"guzva/internal/store"
)

// Retention bounds the visit tables: a year of "how is my page doing".
const Retention = 400 * 24 * time.Hour

// Flush drains the recorder into the store (the sched job, once a minute).
// Rows for slugs that are not a venue — a stale bookmark to a deleted
// venue — are dropped. On a store error the rest of the batch is put back,
// so a transient failure loses nothing.
func (r *Recorder) Flush(ctx context.Context, st *store.Store) error {
	hours, days := r.Drain()
	ids := map[string]int64{}
	id := func(slug string) (int64, error) {
		if v, ok := ids[slug]; ok {
			return v, nil
		}
		v, err := st.VenueBySlug(ctx, slug)
		if errors.Is(err, store.ErrNotFound) {
			ids[slug] = 0
			return 0, nil
		}
		if err != nil {
			return 0, err
		}
		ids[slug] = v.ID
		return v.ID, nil
	}
	for i, h := range hours {
		vid, err := id(h.Slug)
		if err == nil && vid != 0 {
			err = st.AddVisitHour(ctx, vid, time.Unix(h.Hour, 0), h.Views, h.Polls, h.QR)
		}
		if err != nil {
			r.Restore(hours[i:], days)
			return err
		}
	}
	for i, d := range days {
		vid, err := id(d.Slug)
		if err == nil && vid != 0 {
			err = st.SetVisitDay(ctx, vid, d.Day, d.Visitors)
		}
		if err != nil {
			r.Restore(nil, days[i:])
			return err
		}
	}
	return nil
}
