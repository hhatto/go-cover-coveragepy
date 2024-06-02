package main

import (
	"fmt"
	"math"
)

func getPercentageValue(numerator, denominator uint, precision int) string {
	var value float64
	if denominator == 0 {
		value = 0.0
	} else {
		value = float64(numerator) / float64(denominator) * 100
	}

	if precision <= 0 {
		return fmt.Sprintf("%.0f", math.Round(value))
	}
	fmtString := fmt.Sprintf("%%.%df", precision)
	return fmt.Sprintf(fmtString, value)
}
