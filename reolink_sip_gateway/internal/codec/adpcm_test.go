package codec

import "testing"

func TestADPCMEncoderRoundTripBlock(t *testing.T) {
	input := []int16{0, 500, -500, 1000, -1000, 1500, -1500, 2000, -2000, 2500, -2500, 3000, -3000, 3500, -3500, 4000, -4000, 4500}
	enc := &ADPCMEncoder{}
	block, err := enc.EncodeBlock(input)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := len(block), 4+len(input)/2; got != want {
		t.Fatalf("block len=%d want=%d", got, want)
	}
	dec := &ADPCMDecoder{}
	decoded := dec.Decode(block)
	if len(decoded) != len(input) {
		t.Fatalf("decoded samples=%d want=%d", len(decoded), len(input))
	}
	var total int64
	for i := range input {
		d := int64(input[i]) - int64(decoded[i])
		if d < 0 {
			d = -d
		}
		total += d
	}
	if avg := total / int64(len(input)); avg > 2500 {
		t.Fatalf("average reconstruction error=%d", avg)
	}
}

func TestADPCMEncoderRejectsOddBlock(t *testing.T) {
	_, err := (&ADPCMEncoder{}).EncodeBlock([]int16{1, 2, 3})
	if err == nil {
		t.Fatal("expected odd sample count error")
	}
}
