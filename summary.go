package main

import (
	"fmt"
	"io"
)

func writeSummary(out io.Writer, results []CheckResult) error {
	counts := make(map[string]int)
	canceled := 0
	for _, result := range results {
		counts[result.State]++
		if result.ErrorKind == "canceled" {
			canceled++
		}
	}
	_, err := fmt.Fprintf(out,
		"SUMMARY total=%d up=%d down=%d slow=%d mismatch=%d expiring=%d canceled=%d\n",
		len(results), counts["UP"], counts["DOWN"], counts["SLOW"],
		counts["MISMATCH"], counts["EXPIRING"], canceled)
	return err
}
