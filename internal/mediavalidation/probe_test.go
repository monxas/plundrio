package mediavalidation

import "testing"

func TestStderrHasErrors(t *testing.T) {
	for _, c := range []struct {
		stderr string
		want   bool
	}{
		{"", false},
		{"\n", false},
		// Seen on 'The Runner (2026)' (YTS mp4), plundrio-ecosystem#39.
		{"[mov,mp4,m4a,3gp,3g2,mj2 @ 0x55d0c1a2b3c0] Referenced QT chapter track not found\n", false},
		{"[mov,mp4,m4a,3gp,3g2,mj2 @ 0x1] Referenced QT chapter track not found\n[mov,mp4,m4a,3gp,3g2,mj2 @ 0x1] Referenced QT chapter track not found\n", false},
		{"[mov,mp4,m4a,3gp,3g2,mj2 @ 0x1] moov atom not found\n", true},
		{"[mov,mp4,m4a,3gp,3g2,mj2 @ 0x1] Referenced QT chapter track not found\n[h264 @ 0x2] Invalid NAL unit size\n", true},
		{"Referenced QT chapter track not found, and more", true},
		{"[x @ 0x1] Referenced QT chapter track not found extra", true},
		{"garbage", true},
	} {
		if got := stderrHasErrors([]byte(c.stderr)); got != c.want {
			t.Errorf("stderrHasErrors(%q) = %v, want %v", c.stderr, got, c.want)
		}
	}
}
