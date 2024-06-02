package main

import (
	"fmt"
	"math"
)

func getPercentageValue(value float64, precision int) string {
	if precision <= 0 {
		return fmt.Sprintf("%.0f", math.Round(value))
	}
	fmtString := fmt.Sprintf("%%.%df", precision)
	return fmt.Sprintf(fmtString, value)
}
