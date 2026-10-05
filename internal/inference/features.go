package inference

import (
	"math"

	"gonum.org/v1/gonum/dsp/fourier"
)

func bfloat16(value float64) float64 {
	raw := math.Float32bits(float32(value))
	// Round-to-nearest-even before discarding the lower 16 mantissa bits.
	raw += 0x7fff + ((raw >> 16) & 1)
	return float64(math.Float32frombits(raw & 0xffff0000))
}

func hzToMel(v float64) float64 { return 2595 * math.Log10(1+v/700) }
func melToHz(v float64) float64 { return 700 * (math.Pow(10, v/2595) - 1) }

// melBank matches torchaudio's HTK triangular filter bank closely enough for
// the exported GigaAM and WeSpeaker models (no Slaney normalization).
func melBank(nFFT, sampleRate, count int) [][]float64 {
	bins := nFFT/2 + 1
	bank := make([][]float64, bins)
	for i := range bank {
		bank[i] = make([]float64, count)
	}
	minMel, maxMel := hzToMel(0), hzToMel(float64(sampleRate)/2)
	points := make([]float64, count+2)
	for i := range points {
		points[i] = melToHz(minMel + (maxMel-minMel)*float64(i)/float64(count+1))
	}
	for b := 0; b < bins; b++ {
		freq := float64(b*sampleRate) / float64(nFFT)
		for m := 0; m < count; m++ {
			if freq >= points[m] && freq <= points[m+1] {
				bank[b][m] = (freq - points[m]) / (points[m+1] - points[m])
			} else if freq > points[m+1] && freq <= points[m+2] {
				bank[b][m] = (points[m+2] - freq) / (points[m+2] - points[m+1])
			}
		}
	}
	return bank
}

func powerSpectrum(frame []float64, nFFT int) []float64 {
	fft := fourier.NewFFT(nFFT)
	coeff := fft.Coefficients(nil, frame)
	out := make([]float64, nFFT/2+1)
	for i, v := range coeff {
		out[i] = real(v)*real(v) + imag(v)*imag(v)
	}
	return out
}

// GigaAMFeatures returns channel-major [64,T] log-mel features.
func GigaAMFeatures(samples []float32) ([]float32, int) {
	const nFFT, hop, melCount = 320, 160, 64
	if len(samples) < nFFT {
		padded := make([]float32, nFFT)
		copy(padded, samples)
		samples = padded
	}
	frames := 1 + (len(samples)-nFFT)/hop
	result := make([]float32, melCount*frames)
	bank := melBank(nFFT, 16000, melCount)
	for bin := range bank {
		for mel := range bank[bin] {
			bank[bin][mel] = bfloat16(bank[bin][mel])
		}
	}
	window := make([]float64, nFFT)
	for i := range window {
		window[i] = bfloat16(0.5 - 0.5*math.Cos(2*math.Pi*float64(i)/nFFT))
	}
	frame := make([]float64, nFFT)
	for t := 0; t < frames; t++ {
		for i := range frame {
			frame[i] = float64(samples[t*hop+i]) * window[i]
		}
		power := powerSpectrum(frame, nFFT)
		for m := 0; m < melCount; m++ {
			var energy float64
			for b, value := range power {
				energy += value * bank[b][m]
			}
			if energy < 1e-9 {
				energy = 1e-9
			} else if energy > 1e9 {
				energy = 1e9
			}
			result[m*frames+t] = float32(math.Log(energy))
		}
	}
	return result, frames
}

// WeSpeakerFeatures returns frame-major [T,80] Kaldi-compatible fbank values.
func WeSpeakerFeatures(samples []float32) ([]float32, int) {
	const nFFT, win, hop, melCount = 512, 400, 160, 80
	if len(samples) < win {
		return nil, 0
	}
	frames := 1 + (len(samples)-win)/hop
	result := make([]float32, frames*melCount)
	bank := melBank(nFFT, 16000, melCount)
	window := make([]float64, win)
	for i := range window {
		window[i] = 0.54 - 0.46*math.Cos(2*math.Pi*float64(i)/float64(win-1))
	}
	frame := make([]float64, nFFT)
	means := make([]float64, melCount)
	for t := 0; t < frames; t++ {
		var mean float64
		for i := 0; i < win; i++ {
			mean += float64(samples[t*hop+i])
		}
		mean /= win
		for i := range frame {
			frame[i] = 0
		}
		previous := float64(samples[t*hop]) - mean
		for i := 0; i < win; i++ {
			current := float64(samples[t*hop+i]) - mean
			value := current
			if i > 0 {
				value -= .97 * previous
			}
			previous = current
			frame[i] = value * window[i]
		}
		power := powerSpectrum(frame, nFFT)
		for m := 0; m < melCount; m++ {
			var energy float64
			for b, value := range power {
				energy += value * bank[b][m]
			}
			if energy < 1.1920929e-7 {
				energy = 1.1920929e-7
			}
			value := math.Log(energy)
			result[t*melCount+m] = float32(value)
			means[m] += value
		}
	}
	for m := range means {
		means[m] /= float64(frames)
	}
	for t := 0; t < frames; t++ {
		for m := 0; m < melCount; m++ {
			result[t*melCount+m] -= float32(means[m])
		}
	}
	return result, frames
}
