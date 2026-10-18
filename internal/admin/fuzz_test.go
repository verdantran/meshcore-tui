package admin

import "testing"

// FuzzReplies covers what a node sends back: telemetry, region lists and its packet log.
func FuzzReplies(f *testing.F) {
	f.Add([]byte{1, 0x74, 0x01, 0x9a}, "*\n bristol F\n  bath ^\n")
	f.Add([]byte{}, "12:00:01 - 1/1/2026 U: RX, len=20 (type=4, route=F, payload_len=10) SNR=5 RSSI=-90 score=1000 [AA -> BB]")
	f.Fuzz(func(t *testing.T, lpp []byte, text string) {
		DescribeLPP(lpp)
		ParseRegions(text)
		ParseLog(text)
		CompareVersions(text, "v1.2.3")
	})
}
