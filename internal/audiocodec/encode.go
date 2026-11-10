package audiocodec

import (
	"encoding/binary"
	"errors"
	"math"
)

// EncodePCM16 renders a 16-bit integer PCM WAV. Its byte layout is unchanged
// from the original single-format service so existing 16-bit renders stay
// reproducible.
func EncodePCM16(sampleRate, channels int, samples []float32) ([]byte, error) {
	return EncodeWAV(sampleRate, channels, pcmBitWidth16, samples)
}

// EncodePCM24 renders a 24-bit integer PCM WAV.
func EncodePCM24(sampleRate, channels int, samples []float32) ([]byte, error) {
	return EncodeWAV(sampleRate, channels, pcmBitWidth24, samples)
}

// EncodeWAV renders an integer PCM WAV at the requested width (16 or 24 bit).
func EncodeWAV(sampleRate, channels, bitsPerSample int, samples []float32) ([]byte, error) {
	if sampleRate < 8000 || sampleRate > 192000 {
		return nil, errors.New("sample rate is outside the accepted range")
	}
	if channels != 1 && channels != 2 {
		return nil, errors.New("channel count must be one or two")
	}
	if len(samples) == 0 || len(samples)%channels != 0 {
		return nil, errors.New("samples are empty or not frame-aligned")
	}
	bytesPerSample := bitsPerSample / 8
	if bitsPerSample != pcmBitWidth16 && bitsPerSample != pcmBitWidth24 {
		return nil, errors.New("output width must be 16 or 24 bit integer PCM")
	}
	dataBytes := uint64(len(samples)) * uint64(bytesPerSample)
	if dataBytes > math.MaxUint32-36 {
		return nil, errors.New("encoded PCM payload is too large")
	}
	blockAlign := channels * bytesPerSample
	byteCount := 44 + int(dataBytes)
	result := make([]byte, byteCount)
	copy(result[0:4], "RIFF")
	binary.LittleEndian.PutUint32(result[4:8], uint32(byteCount-8))
	copy(result[8:12], "WAVE")
	copy(result[12:16], "fmt ")
	binary.LittleEndian.PutUint32(result[16:20], 16)
	binary.LittleEndian.PutUint16(result[20:22], pcmFormatCode)
	binary.LittleEndian.PutUint16(result[22:24], uint16(channels))
	binary.LittleEndian.PutUint32(result[24:28], uint32(sampleRate))
	binary.LittleEndian.PutUint32(result[28:32], uint32(sampleRate*blockAlign))
	binary.LittleEndian.PutUint16(result[32:34], uint16(blockAlign))
	binary.LittleEndian.PutUint16(result[34:36], uint16(bitsPerSample))
	copy(result[36:40], "data")
	binary.LittleEndian.PutUint32(result[40:44], uint32(dataBytes))

	for index, sample := range samples {
		quantized := quantizeInteger(sample, bitsPerSample)
		offset := 44 + index*bytesPerSample
		switch bitsPerSample {
		case pcmBitWidth16:
			binary.LittleEndian.PutUint16(result[offset:offset+2], uint16(int16(quantized)))
		case pcmBitWidth24:
			encoded := uint32(quantized)
			result[offset] = byte(encoded)
			result[offset+1] = byte(encoded >> 8)
			result[offset+2] = byte(encoded >> 16)
		}
	}
	return result, nil
}

func quantizeInteger(sample float32, bitsPerSample int) int32 {
	negativeBound := int64(-1) << (bitsPerSample - 1)
	positiveBound := -negativeBound - 1
	scalePositive := float64(positiveBound)
	scaleNegative := float64(-negativeBound)
	var quantized int64
	switch {
	case sample <= -1:
		quantized = negativeBound
	case sample >= 1:
		quantized = positiveBound
	case sample < 0:
		quantized = int64(math.Round(float64(sample) * scaleNegative))
	default:
		quantized = int64(math.Round(float64(sample) * scalePositive))
	}
	if quantized < negativeBound {
		quantized = negativeBound
	}
	if quantized > positiveBound {
		quantized = positiveBound
	}
	return int32(quantized)
}
