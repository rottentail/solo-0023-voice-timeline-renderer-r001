package checkflow

import (
	"encoding/binary"
	"math"

	"voice-timeline-renderer/internal/audiocodec"
)

func sineWAV(sampleRate, channels, durationMS int, frequency, amplitude float64) ([]byte, error) {
	samples := sineSamples(sampleRate, channels, durationMS, frequency, amplitude)
	return audiocodec.EncodePCM16(sampleRate, channels, samples)
}

// sineWAV24 renders the same tone as a 24-bit integer PCM WAV.
func sineWAV24(sampleRate, channels, durationMS int, frequency, amplitude float64) ([]byte, error) {
	samples := sineSamples(sampleRate, channels, durationMS, frequency, amplitude)
	return audiocodec.EncodePCM24(sampleRate, channels, samples)
}

// sineWAVFloat32 renders the tone as a 32-bit IEEE float WAV, including values
// that exercise the full float headroom above 0 dBFS integer equivalent.
func sineWAVFloat32(sampleRate, channels, durationMS int, frequency, amplitude float64) ([]byte, error) {
	samples := sineSamples(sampleRate, channels, durationMS, frequency, amplitude)
	return encodeFloat32WAV(sampleRate, channels, samples)
}

func sineSamples(sampleRate, channels, durationMS int, frequency, amplitude float64) []float32 {
	frames := sampleRate * durationMS / 1000
	samples := make([]float32, frames*channels)
	for frame := 0; frame < frames; frame++ {
		seconds := float64(frame) / float64(sampleRate)
		first := float32(math.Sin(2*math.Pi*frequency*seconds) * amplitude)
		if channels == 1 {
			samples[frame] = first
			continue
		}
		second := float32(math.Sin(2*math.Pi*frequency*seconds+0.35) * amplitude * 0.8)
		samples[frame*2] = first
		samples[frame*2+1] = second
	}
	return samples
}

func encodeFloat32WAV(sampleRate, channels int, samples []float32) ([]byte, error) {
	dataBytes := uint64(len(samples)) * 4
	blockAlign := channels * 4
	byteCount := 44 + int(dataBytes)
	result := make([]byte, byteCount)
	copy(result[0:4], "RIFF")
	binary.LittleEndian.PutUint32(result[4:8], uint32(byteCount-8))
	copy(result[8:12], "WAVE")
	copy(result[12:16], "fmt ")
	binary.LittleEndian.PutUint32(result[16:20], 16)
	binary.LittleEndian.PutUint16(result[20:22], 3) // WAVE_FORMAT_IEEE_FLOAT
	binary.LittleEndian.PutUint16(result[22:24], uint16(channels))
	binary.LittleEndian.PutUint32(result[24:28], uint32(sampleRate))
	binary.LittleEndian.PutUint32(result[28:32], uint32(sampleRate*blockAlign))
	binary.LittleEndian.PutUint16(result[32:34], uint16(blockAlign))
	binary.LittleEndian.PutUint16(result[34:36], 32)
	copy(result[36:40], "data")
	binary.LittleEndian.PutUint32(result[40:44], uint32(dataBytes))
	for index, sample := range samples {
		binary.LittleEndian.PutUint32(result[44+index*4:48+index*4], math.Float32bits(sample))
	}
	return result, nil
}
