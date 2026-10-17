package ui

import (
	"math"

	"meshcore-tui/internal/radio"
	"meshcore-tui/internal/theme"
)

type grade int

const (
	ungraded grade = iota
	bad
	average
	good
	veryGood
)

// band grades v against three cut-offs, from very good to bad; lowerBetter flips which end is which.
func band(v float64, lowerBetter bool, cuts [3]float64) grade {
	if lowerBetter {
		v, cuts = -v, [3]float64{-cuts[0], -cuts[1], -cuts[2]}
	}
	switch {
	case v >= cuts[0]:
		return veryGood
	case v >= cuts[1]:
		return good
	case v >= cuts[2]:
		return average
	}
	return bad
}

// healthGrade says how a Health measure is doing. Rates are graded for a repeater: one that hears and
// passes on traffic is placed well. Airtime limits follow the EU's 10% duty cycle.
func (m *Model) healthGrade(node [32]byte, name string, v float64) grade {
	if math.IsNaN(v) {
		return ungraded
	}
	switch name {
	case "Received", "Sent":
		return band(v, false, [3]float64{60, 10, 1})
	case "Duplicates":
		return band(v, true, [3]float64{30, 60, 80})
	case "Receive errors":
		return band(v, true, [3]float64{1, 5, 15})
	case "Transmitting":
		return band(v, true, [3]float64{1, 3, 10})
	case "Receiving":
		return band(v, true, [3]float64{5, 15, 30})
	case "Noise floor":
		return band(v, true, [3]float64{-115, -105, -95})
	case "Battery":
		c := m.chemistry(node)
		if c == radio.Wired {
			return ungraded
		}
		return band(float64(radio.Battery{MilliVolts: uint16(math.Round(v * 1000))}.PercentOf(c)), false, [3]float64{60, 30, 15})
	}
	return ungraded
}

// gradeColour is pale green for good, strong green for very good, amber for average and red for bad.
func (m *Model) gradeColour(g grade) theme.RGB {
	th := m.th
	switch g {
	case veryGood:
		return th.Green
	case good:
		pale := th.Text
		if luma(th.Bg) > luma(th.Text) {
			pale = th.Bg
		}
		return th.Green.Lerp(pale, 0.5)
	case average:
		return th.Yellow
	case bad:
		return th.Red
	}
	return th.Cyan
}

func luma(c theme.RGB) float64 {
	return 0.2126*float64(c.R) + 0.7152*float64(c.G) + 0.0722*float64(c.B)
}
