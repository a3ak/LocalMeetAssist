package inference

import "testing"

func TestFeatureShapes(t *testing.T) {
	samples := make([]float32, 16000)
	for i := range samples {
		samples[i] = float32(i%97)/97 - .5
	}
	giga, gigaFrames := GigaAMFeatures(samples)
	if gigaFrames != 99 || len(giga) != 64*gigaFrames {
		t.Fatalf("unexpected GigaAM features: frames=%d len=%d", gigaFrames, len(giga))
	}
	wespeaker, speakerFrames := WeSpeakerFeatures(samples)
	if speakerFrames != 98 || len(wespeaker) != 80*speakerFrames {
		t.Fatalf("unexpected WeSpeaker features: frames=%d len=%d", speakerFrames, len(wespeaker))
	}
}
