package admin

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"guzva/internal/i18n"
	"guzva/internal/store"
	"guzva/internal/venue"
)

// The owner panel's numbers: how many people looked at the page, on which
// days and hours, the four-week trend, and whether the camera is doing its
// job. Computed here from counts — the store holds no visitor identities
// (internal/stats). Charts are SVG with attribute geometry: the consoles'
// CSP has no 'unsafe-inline', so nothing here may become a style attribute.

// kpi is one headline number with the week before for comparison. Two plain
// numbers read faster than a percentage and stay honest at small counts.
type kpi struct {
	Label string
	Value int64
	Prev  int64
	Up    bool // more than the week before
	Down  bool // fewer than the week before
}

// bar is one SVG column in viewBox units.
type bar struct {
	X, Y, W, H int
	Title      string // native tooltip
}

// gridLine is one horizontal value line with its axis label.
type gridLine struct {
	Y     int
	Label string
}

// dayGroup is one weekday in the this-week-vs-last-week chart.
type dayGroup struct {
	Label   string // "Po", "Ut" … in the venue's language
	Date    string // "15.10."
	Today   bool
	Cur     bar // this week's day
	Prev    bar // the same weekday last week (drawn lighter)
	HasPrev bool
	Value   string // this week's count, printed above the bar
	ValueY  int
	CX      int // centre of the group, for the text anchors
	ValueX  int
}

type hourBar struct {
	bar
	Peak   bool
	Value  string // printed above the peak only
	ValueY int
	CX     int
}

type tick struct {
	X     int
	Label string
}

// band is a shaded x-range (today's opening hours behind the hour chart).
type band struct{ X, W int }

// trend is the 28-day daily line with a 7-day average.
type trend struct {
	Area    string // SVG path, filled
	Line    string // polyline points, daily views
	Avg     string // polyline points, trailing 7-day average
	TodayX  int
	TodayY  int
	Weeks   []tick // separators every 7 days, labelled with the date
	Grid    []gridLine
	Total   int64 // views over the 28 days
	HasData bool
}

type stripCell struct {
	X  int
	On bool
}

type ownerStats struct {
	Live      int
	KPIs      []kpi
	Week      []dayGroup
	WeekGrid  []gridLine
	Hours     []hourBar
	HourGrid  []gridLine
	HourTicks []tick
	Open      []band
	Peak      string
	Trend     trend
	HasData   bool
	Uptime    int // % of 10-minute slots since `since` holding a frame; -1 = too new to say
	Strip     []stripCell
	StripFrom string // label for the strip's left edge ("prije 24 h" / since install)
}

// Chart geometry (viewBox units). The left gutter holds the value labels.
const (
	gutter = 26

	dayTop, dayBase     = 14, 100 // bars between; weekday + date labels below
	hourTop, hourBase   = 12, 72
	trendTop, trendBase = 10, 90
	trendW              = 560 // full card width: a wider viewBox keeps its type the same size as the half-width charts
)

// niceMax rounds a chart maximum up to 1, 2 or 5 × 10ⁿ so the gridline
// labels are round numbers.
func niceMax(m int64) int64 {
	if m < 4 {
		return 4
	}
	p := int64(1)
	for p*10 <= m {
		p *= 10
	}
	for _, f := range []int64{1, 2, 5, 10} {
		if f*p >= m {
			return f * p
		}
	}
	return 10 * p
}

func grid(top, base int, max int64) []gridLine {
	return []gridLine{
		{Y: base, Label: "0"},
		{Y: (top + base) / 2, Label: strconv.FormatInt(max/2, 10)},
		{Y: top, Label: strconv.FormatInt(max, 10)},
	}
}

// buildOwnerStats turns raw counts into the panel. hours must cover the
// last 28 local days; frames the uptime window, which starts at since (the
// later of 24 h ago and the camera's install). sched is the venue's weekly
// schedule (today's opening hours are shaded on the hour chart).
func buildOwnerStats(now time.Time, loc *time.Location, hours []store.VisitHour, days []store.VisitDay,
	frames []time.Time, since time.Time, live int, sched venue.Hours, cat *i18n.Catalog) ownerStats {
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
	// read as "down" every morning. Today lives in the charts instead.
	add := func(label string, a [28]int64) {
		cur, prev := sum(a, 1, 8), sum(a, 8, 15)
		st.KPIs = append(st.KPIs, kpi{Label: label, Value: cur, Prev: prev, Up: cur > prev, Down: cur < prev})
	}
	add(cat.Get("owner.kpi.visitors"), visitors)
	add(cat.Get("owner.kpi.views"), views)
	add(cat.Get("owner.kpi.qr"), qr)
	st.HasData = sum(views, 0, 28) > 0

	// This week vs last week, one group per day, oldest left, today right.
	var maxDay int64
	for i := 0; i < 14; i++ {
		maxDay = max(maxDay, views[i])
	}
	dmax := niceMax(maxDay)
	st.WeekGrid = grid(dayTop, dayBase, dmax)
	height := func(n int64, top, base int, m int64) int {
		h := int(n * int64(base-top) / m)
		if n > 0 && h < 2 {
			h = 2
		}
		return h
	}
	names := cat.DayNames()
	for g := 0; g < 7; g++ {
		i := 6 - g // days ago
		d := today.AddDate(0, 0, -i)
		x := gutter + 2 + g*39
		ch, ph := height(views[i], dayTop, dayBase, dmax), height(views[i+7], dayTop, dayBase, dmax)
		st.Week = append(st.Week, dayGroup{
			Label: string([]rune(names[venue.DayIdx(d)])[:2]), Date: d.Format("2.1."), Today: i == 0,
			Cur:     bar{X: x + 17, W: 17, H: ch, Y: dayBase - ch, Title: cat.F("owner.chart.daytip", d.Format("02.01."), strconv.FormatInt(views[i], 10))},
			Prev:    bar{X: x + 4, W: 12, H: ph, Y: dayBase - ph, Title: cat.F("owner.chart.prevtip", strconv.FormatInt(views[i+7], 10))},
			HasPrev: views[i+7] > 0,
			Value:   strconv.FormatInt(views[i], 10),
			ValueY:  dayBase - ch - 3,
			CX:      x + 19,
			ValueX:  x + 25,
		})
	}

	// Hour of day over 28 days; the peak gets its number; today's opening
	// hours are shaded behind the bars.
	var maxHour int64
	peak := -1
	for h, n := range byHour {
		maxHour = max(maxHour, n)
		if n > 0 && (peak < 0 || n > byHour[peak]) {
			peak = h
		}
	}
	hmax := niceMax(maxHour)
	st.HourGrid = grid(hourTop, hourBase, hmax)
	const hw = 11 // viewBox units per hour
	for h, n := range byHour {
		bh := height(n, hourTop, hourBase, hmax)
		hb := hourBar{bar: bar{X: gutter + h*hw + 1, W: hw - 2, H: bh, Y: hourBase - bh,
			Title: cat.F("owner.chart.hourtip", strconv.Itoa(h), strconv.FormatInt(n, 10))},
			Peak: h == peak, CX: gutter + h*hw + hw/2}
		if hb.Peak {
			hb.Value, hb.ValueY = strconv.FormatInt(n, 10), hourBase-bh-3
		}
		st.Hours = append(st.Hours, hb)
	}
	for h := 0; h <= 24; h += 3 {
		st.HourTicks = append(st.HourTicks, tick{X: gutter + h*hw, Label: strconv.Itoa(h)})
	}
	for _, r := range sched[local.Weekday()] {
		x0 := gutter + r.OpenMin*hw/60
		x1 := gutter + min(r.CloseMin, 24*60)*hw/60
		if x1 > x0 {
			st.Open = append(st.Open, band{X: x0, W: x1 - x0})
		}
	}
	if peak >= 0 && sum(views, 0, 28) >= 10 {
		st.Peak = cat.F("owner.peak", strconv.Itoa(peak), strconv.Itoa((peak+1)%24))
	}

	// 28-day trend, oldest left, with a trailing 7-day average.
	var maxT int64
	for _, n := range views {
		maxT = max(maxT, n)
	}
	tmax := niceMax(maxT)
	st.Trend.Grid = grid(trendTop, trendBase, tmax)
	tx := func(i int) int { return gutter + (27-i)*trendW/27 } // i = days ago
	ty := func(v int64) int { return trendBase - int(v*int64(trendBase-trendTop)/tmax) }
	var line, avg []string
	area := fmt.Sprintf("M%d %d", tx(27), trendBase)
	for i := 27; i >= 0; i-- {
		p := fmt.Sprintf("%d,%d", tx(i), ty(views[i]))
		line = append(line, p)
		area += " L" + strings.Replace(p, ",", " ", 1)
		if i <= 21 { // a full 7-day window behind it
			a := sum(views, i, i+7) / 7
			avg = append(avg, fmt.Sprintf("%d,%d", tx(i), ty(a)))
		}
		if i%7 == 6 && i != 27 {
			st.Trend.Weeks = append(st.Trend.Weeks, tick{X: tx(i), Label: today.AddDate(0, 0, -i).Format("2.1.")})
		}
	}
	area += fmt.Sprintf(" L%d %d Z", tx(0), trendBase)
	st.Trend.Area, st.Trend.Line, st.Trend.Avg = area, strings.Join(line, " "), strings.Join(avg, " ")
	st.Trend.TodayX, st.Trend.TodayY = tx(0), ty(views[0])
	st.Trend.Total = sum(views, 0, 28)
	st.Trend.HasData = st.Trend.Total > 0

	// Uptime: 10-minute slots since `since` holding a frame, shown as a
	// strip (each slot one cell) and summarised as a percentage.
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
	for i, ok := range slots {
		if ok {
			n++
		}
		// Right-align the strip: the newest slot always ends at the right
		// edge, so a camera installed today fills from the right.
		st.Strip = append(st.Strip, stripCell{X: (144 - nSlots + i) * 2, On: ok})
	}
	st.Uptime = (n*100 + nSlots/2) / nSlots
	if nSlots >= 144 {
		st.StripFrom = cat.Get("owner.strip.day") // a bare "13:02" would mean yesterday
	} else {
		st.StripFrom = cat.F("owner.strip.from", cat.Time(since.In(loc))) // installed today
	}
	return st
}
