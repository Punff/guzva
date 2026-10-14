package admin

import (
	"strconv"
	"time"

	"guzva/internal/i18n"
	"guzva/internal/stats"
	"guzva/internal/store"
)

// The owner panel's statistics: what the page earns the venue (views,
// visitors, minutes watched, QR scans), when guests look, and whether the
// camera is doing its job. Everything is computed here from counts — the
// store holds no visitor identities (internal/stats).

type kpi struct {
	Label string
	Value int64
	Trend string // "+12 % od prošlog tjedna", "" when there is nothing to compare
	Up    bool
	Down  bool
}

// bar is one SVG chart column in viewBox units. Attributes, not inline
// styles: the consoles' CSP has no 'unsafe-inline'.
type bar struct {
	X, Y, W, H int
	Title      string // native tooltip: date/hour + count
	Label      string // axis label under the bar, "" for none
	Hi         bool   // today / the peak hour
}

type ownerStats struct {
	Live     int
	KPIs     []kpi
	Days     []bar
	Hours    []bar
	Peak     string
	Insights []string
	Uptime   int // % of 10-minute slots since `since` holding a frame; -1 = too new to say
	HasData  bool
}

const (
	dayChartH  = 100
	hourChartH = 70
)

// buildOwnerStats turns raw counts into the panel. hours must cover the
// last 28 local days; frames the uptime window, which starts at since (the
// later of 24 h ago and the camera's install, so a new camera is not
// reported as "0 % online").
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

	var views, polls, qr [28]int64
	var byHour [24]int64
	for _, h := range hours {
		i := dayIdx(h.Hour)
		if i < 0 || i >= 28 {
			continue
		}
		views[i] += h.Views
		polls[i] += h.Polls
		qr[i] += h.QR
		byHour[h.Hour.In(loc).Hour()] += h.Views
	}
	var visitors [28]int64
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
	week := func(a [28]int64) (int64, int64) { return sum(a, 0, 7), sum(a, 7, 14) }

	add := func(label string, cur, prev int64) {
		k := kpi{Label: label, Value: cur}
		switch {
		case prev == 0 && cur > 0:
			k.Trend, k.Up = cat.Get("owner.trend.new"), true
		case prev == 0:
		case cur == prev:
			k.Trend = cat.Get("owner.trend.flat")
		default:
			pct := (cur - prev) * 100 / prev
			if pct >= 0 {
				k.Trend, k.Up = cat.F("owner.trend.up", strconv.FormatInt(pct, 10)), true
			} else {
				k.Trend, k.Down = cat.F("owner.trend.down", strconv.FormatInt(-pct, 10)), true
			}
		}
		st.KPIs = append(st.KPIs, k)
	}
	vc, vp := week(views)
	uc, up := week(visitors)
	pc, pp := week(polls)
	qc, qp := week(qr)
	add(cat.Get("owner.kpi.views"), vc, vp)
	add(cat.Get("owner.kpi.visitors"), uc, up)
	add(cat.Get("owner.kpi.minutes"), pc*stats.PollSeconds/60, pp*stats.PollSeconds/60)
	add(cat.Get("owner.kpi.qr"), qc, qp)
	st.HasData = sum(views, 0, 28) > 0

	// 14-day bars, oldest left.
	var maxDay int64 = 1
	for i := 0; i < 14; i++ {
		maxDay = max(maxDay, views[i])
	}
	for i := 13; i >= 0; i-- {
		d := today.AddDate(0, 0, -i)
		h := int(views[i] * (dayChartH - 12) / maxDay)
		if views[i] > 0 && h < 2 {
			h = 2
		}
		b := bar{X: (13-i)*20 + 2, W: 16, H: h, Y: dayChartH - 12 - h, Hi: i == 0,
			Title: cat.F("owner.chart.daytip", d.Format("02.01."), strconv.FormatInt(views[i], 10), strconv.FormatInt(visitors[i], 10))}
		if i%2 == 0 {
			b.Label = d.Format("2.")
		}
		st.Days = append(st.Days, b)
	}

	// Hour-of-day profile over 28 days, with the peak highlighted.
	var maxHour int64 = 1
	peak := -1
	for h, n := range byHour {
		if n > maxHour {
			maxHour = n
		}
		if n > 0 && (peak < 0 || n > byHour[peak]) {
			peak = h
		}
	}
	for h, n := range byHour {
		bh := int(n * (hourChartH - 12) / maxHour)
		if n > 0 && bh < 2 {
			bh = 2
		}
		b := bar{X: h*10 + 1, W: 8, H: bh, Y: hourChartH - 12 - bh, Hi: h == peak,
			Title: cat.F("owner.chart.hourtip", strconv.Itoa(h), strconv.FormatInt(n, 10))}
		if h%6 == 0 {
			b.Label = strconv.Itoa(h)
		}
		st.Hours = append(st.Hours, b)
	}
	if peak >= 0 && sum(views, 0, 28) >= 10 {
		st.Peak = cat.F("owner.peak", strconv.Itoa(peak), strconv.Itoa((peak+1)%24))
	}

	// Insights: one nudge per line, the useful ones first.
	if !st.HasData {
		st.Insights = append(st.Insights, cat.Get("owner.insight.empty"))
	}
	if qc == 0 {
		st.Insights = append(st.Insights, cat.Get("owner.insight.qr.none"))
	} else {
		st.Insights = append(st.Insights, cat.F("owner.insight.qr", strconv.FormatInt(qc, 10)))
	}
	if vc > 0 {
		best := 0
		for i := 1; i < 7; i++ {
			if views[i] > views[best] {
				best = i
			}
		}
		wd := (int(today.AddDate(0, 0, -best).Weekday()) + 6) % 7 // Mo-first
		st.Insights = append(st.Insights, cat.F("owner.insight.best", cat.DayNames()[wd], strconv.FormatInt(views[best], 10)))
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
