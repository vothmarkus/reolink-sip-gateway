package acousticmarker

import (
	"math"
	"testing"
)

func TestCalibrationAndConnectionMarkerDurations(t *testing.T) {
	for _, rate := range []int{8000, 16000, 48000} {
		calibration := Calibration(rate)
		connection := Connection(rate)
		if got, want := len(calibration), int(math.Round(CalibrationDuration.Seconds()*float64(rate))); got != want {
			t.Fatalf("calibration rate=%d samples=%d want=%d", rate, got, want)
		}
		if got, want := len(connection), int(math.Round(ConnectionDuration.Seconds()*float64(rate))); got != want {
			t.Fatalf("connection rate=%d samples=%d want=%d", rate, got, want)
		}
		for i := range connection {
			if connection[i] != calibration[i] {
				t.Fatalf("connection marker diverges from calibration prefix at rate=%d sample=%d", rate, i)
			}
		}
		if connection[0] != 0 || connection[len(connection)-1] != 0 {
			t.Fatalf("connection marker edges are not faded to zero at rate=%d", rate)
		}
	}
}

func TestInvalidMarkerRateIsRejected(t *testing.T) {
	if Calibration(0) != nil || Connection(-1) != nil {
		t.Fatal("invalid sample rates must not produce a marker")
	}
}
