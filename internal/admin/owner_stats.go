package admin

import (
	"strconv"
	"time"

	"guzva/internal/i18n"
	"guzva/internal/store"
)

// The owner panel's numbers: how many people looked at the page this week
// and last week, on which days, at which hours, and whether the camera is
// doing its job. Computed here from counts — the store holds no visitor
// identities (internal/stats).

// kpi is one headline number with last week's for comparison. Two plain
// numbers read faster than a percentage and stay honest at small counts.
type kpi struct {
	Label string
	Value int64
	Prev  int64
	Up    bool // more than last week
	Down  bool // fewer than last week
}

// bar is one SVG column in viewBox units. Attributes, not inline styles:
// the consoles' CSP has no 'unsafe-inline'.
type bar struct {
	X, Y, W, H int
	Title      string // native tooltip
}

// dayGroup is one weekday in the this-week-vs-last-week chart.
type dayGroup struct {
	Label   string // "Po", "Ut" … in the venue's language
	Today   bool
	Cur     bar    // this week's day
	Prev    bar    // the same weekday last week (drawn faint)
	Value   string // this week's count, printed above the bar
	ValueY  int
	LabelX  int // centre of the group, for the text anchors
	ValueX  int
	HasPrev bool
}

type hourBar struct {
	bar
	Peak   bool
	Label  string // axis label every 6 hours
	Value  string // printed above the peak only
	ValueY int
	CX     int
}

type ownerStats struct {
	Live    int
	KPIs    []kpi
	Week    []dayGroup
	Hours   []hourBar
	Peak    string
	HasData bool
	Uptime  int // % of 10-minute slots since `since` holding a frame; -1 = too new to say
}

const (
	dayChartTop  = 12 // room for the value labels
	dayChartBase = 84 // bar baseline; weekday labels sit below
	hourChartH   = 60
)

// buildOwnerStats turns raw counts into the panel. hours must cover the
// last 14 local days (28 for the hour profile); frames the uptime window,
// which starts at since (the later of 24 h ago and the camera's install).
func buildOwnerStats(now time.Time, loc *time.Location, hours []store.VisitHour, days []store.VisitDay,
	frames []time.Time, since time.Time, live int, cat *i18n.Catalog) ownerStats {
	st := ownerStats{Live: live}
	local := now.In(loc)
	today := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc)
	dayIdx := func(t time.Time) int { // 0 = today, 1 = yesterday …
		lt := t.In(loc)
		d := time.Date(lt.Year(), lt.Month(), lt.Day(), 0, 0, 0, 0, loc)
		return int(today.Sub(d).Hours()/24 + 0.5)
	}

	var views, qr, visitors [28]int64
	var byHour [24]int64
	for _, h := range hours {
		i := dayIdx(h.Hour)
		if i < 0 || i >= 28 {
			continue
		}
		views[i] += h.Views
		qr[i] += h.QR
		byHour[h.Hour.In(loc).Hour()] += h.Views
	}
	for _, d := range days {
		t, err := time.ParseInLocation("2006-01-02", d.Day, loc)
		if err != nil {
			continue
		}
		if i := dayIdx(t); i >= 0 && i < 28 {
			visitors[i] += d.Visitors
		}
	}
	sum := func(a [28]int64, from, to int) (n int64) {
		for i := from; i < to; i++ {
			n += a[i]
		}
		return n
	}
	// The headline numbers compare the last 7 COMPLETE days with the 7
	// before: counting today's half-finished day against a full one would
	// read as "down" every morning. Today lives in the chart instead.
	add := func(label string, a [28]int64) {
		cur, prev := sum(a, 1, 8), sum(a, 8, 15)
		st.KPIs = append(st.KPIs, kpi{Label: label, Value: cur, Prev: prev, Up: cur > prev, Down: cur < prev})
	}
	add(cat.Get("owner.kpi.visitors"), visitors)
	add(cat.Get("owner.kpi.views"), views)
	add(cat.Get("owner.kpi.qr"), qr)
	st.HasData = sum(views, 0, 28) > 0

	// This week vs last week, one group per day, oldest left, today right.
	var maxDay int64 = 1
	for i := 0; i < 14; i++ {
		maxDay = max(maxDay, views[i])
	}
	height := func(n int64) int {
		h := int(n * (dayChartBase - dayChartTop) / maxDay)
		if n > 0 && h < 2 {
			h = 2
		}
		return h
	}
	names := cat.DayNames()
	for g := 0; g < 7; g++ {
		i := 6 - g // days ago
		d := today.AddDate(0, 0, -i)
		wd := (int(d.Weekday()) + 6) % 7 // Mo-first
		x := g * 40
		ch, ph := height(views[i]), height(views[i+7])
		grp := dayGroup{
			Label: string([]rune(names[wd])[:2]), Today: i == 0,
			Cur:     bar{X: x + 17, W: 18, H: ch, Y: dayChartBase - ch, Title: cat.F("owner.chart.daytip", d.Format("02.01."), strconv.FormatInt(views[i], 10))},
			Prev:    bar{X: x + 5, W: 12, H: ph, Y: dayChartBase - ph, Title: cat.F("owner.chart.prevtip", strconv.FormatInt(views[i+7], 10))},
			Value:   strconv.FormatInt(views[i], 10),
			ValueY:  dayChartBase - ch - 3,
			LabelX:  x + 20,
			ValueX:  x + 26,
			HasPrev: views[i+7] > 0,
		}
		st.Week = append(st.Week, grp)
	}

	// Hour of day over 28 days; the peak gets its number.
	var maxHour int64 = 1
	peak := -1
	for h, n := range byHour {
		maxHour = max(maxHour, n)
		if n > 0 && (peak < 0 || n > byHour[peak]) {
			peak = h
		}
	}
	for h, n := range byHour {
		bh := int(n * (hourChartH - 12) / maxHour)
		if n > 0 && bh < 2 {
			bh = 2
		}
		hb := hourBar{bar: bar{X: h*10 + 1, W: 8, H: bh, Y: hourChartH - bh,
			Title: cat.F("owner.chart.hourtip", strconv.Itoa(h), strconv.FormatInt(n, 10))},
			Peak: h == peak, CX: h*10 + 5}
		if h%6 == 0 {
			hb.Label = strconv.Itoa(h)
		}
		if hb.Peak {
			hb.Value, hb.ValueY = strconv.FormatInt(n, 10), hourChartH-bh-3
		}
		st.Hours = append(st.Hours, hb)
	}
	if peak >= 0 && sum(views, 0, 28) >= 10 {
		st.Peak = cat.F("owner.peak", strconv.Itoa(peak), strconv.Itoa((peak+1)%24))
	}

	// Uptime: share of 10-minute slots since `since` holding a frame.
	if since.Before(now.Add(-24 * time.Hour)) {
		since = now.Add(-24 * time.Hour)
	}
	nSlots := int(now.Sub(since) / (10 * time.Minute))
	if nSlots < 1 {
		st.Uptime = -1
		return st
	}
	slots := make([]bool, nSlots)
	for _, f := range frames {
		if i := int(f.Sub(since) / (10 * time.Minute)); i >= 0 && i < nSlots {
			slots[i] = true
		}
	}
	n := 0
	for _, ok := range slots {
		if ok {
			n++
		}
	}
	st.Uptime = (n*100 + nSlots/2) / nSlots
	return st
}
