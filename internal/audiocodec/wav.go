package audiocodec

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
)

const (
	pcmFormatCode    = 1
	ieeeFormatCode   = 3
	extFormatCode    = 0xFFFE
	pcmSubformatTag  = 0x0001
	ieeeSubformatTag = 0x0003
)

// KSDATAFORMAT_SUBTYPE_PCM and KSDATAFORMAT_SUBTYPE_IEEE_FLOAT share this
// 14-byte tail; only the first two bytes carry the format tag.
var subformatTail = [14]byte{
	0x00, 0x00, 0x00, 0x00, 0x10, 0x00,
	0x80, 0x00, 0x00, 0xaa, 0x00, 0x38, 0x9b, 0x71,
}

func DecodeWAV(data []byte) (*Clip, WAVInfo, error) {
	if len(data) < 12 {
		return nil, WAVInfo{}, errors.New("file is shorter than a RIFF header")
	}
	if string(data[0:4]) != "RIFF" || string(data[8:12]) != "WAVE" {
		return nil, WAVInfo{}, errors.New("file is not a RIFF/WAVE payload")
	}

	riffSize := uint64(binary.LittleEndian.Uint32(data[4:8]))
	if riffSize+8 > uint64(len(data)) {
		return nil, WAVInfo{}, errors.New("RIFF size exceeds the supplied payload")
	}

	var formatChunk []byte
	var sampleChunk []byte
	offset := 12
	for offset+8 <= len(data) {
		chunkID := string(data[offset : offset+4])
		chunkSize := int(binary.LittleEndian.Uint32(data[offset+4 : offset+8]))
		payloadStart := offset + 8
		payloadEnd := payloadStart + chunkSize
		if chunkSize < 0 || payloadEnd < payloadStart || payloadEnd > len(data) {
			return nil, WAVInfo{}, fmt.Errorf("chunk %q exceeds the payload", chunkID)
		}
		switch chunkID {
		case "fmt ":
			formatChunk = data[payloadStart:payloadEnd]
		case "data":
			sampleChunk = data[payloadStart:payloadEnd]
		}
		offset = payloadEnd
		if chunkSize%2 == 1 {
			offset++
		}
	}
	if len(formatChunk) < 16 {
		return nil, WAVInfo{}, errors.New("fmt chunk is missing or too short")
	}
	if sampleChunk == nil {
		return nil, WAVInfo{}, errors.New("data chunk is missing")
	}

	audioFormat := binary.LittleEndian.Uint16(formatChunk[0:2])
	channels := int(binary.LittleEndian.Uint16(formatChunk[2:4]))
	sampleRate := int(binary.LittleEndian.Uint32(formatChunk[4:8]))
	byteRate := binary.LittleEndian.Uint32(formatChunk[8:12])
	blockAlign := int(binary.LittleEndian.Uint16(formatChunk[12:14]))
	bitsPerSample := int(binary.LittleEndian.Uint16(formatChunk[14:16]))

	effectiveTag, validBits, err := resolveFormat(audioFormat, bitsPerSample, formatChunk)
	if err != nil {
		return nil, WAVInfo{}, err
	}
	if channels != 1 && channels != 2 {
		return nil, WAVInfo{}, fmt.Errorf("unsupported channel count %d", channels)
	}
	if sampleRate < 8000 || sampleRate > 192000 {
		return nil, WAVInfo{}, fmt.Errorf("sample rate %d is outside the accepted range", sampleRate)
	}
	expectedBlockAlign := channels * (bitsPerSample / 8)
	if blockAlign != expectedBlockAlign {
		return nil, WAVInfo{}, errors.New("block alignment does not match channels and sample width")
	}
	expectedByteRate := uint32(sampleRate * blockAlign)
	if byteRate != expectedByteRate {
		return nil, WAVInfo{}, errors.New("byte rate does not match sample rate and block alignment")
	}
	if len(sampleChunk) == 0 || len(sampleChunk)%blockAlign != 0 {
		return nil, WAVInfo{}, errors.New("sample data is empty or not frame-aligned")
	}

	sampleFormat := FormatPCMInteger
	switch effectiveTag {
	case pcmSubformatTag:
		if validBits != 16 && validBits != 24 {
			return nil, WAVInfo{}, fmt.Errorf("unsupported integer PCM width %d", validBits)
		}
		if validBits != bitsPerSample {
			return nil, WAVInfo{}, errors.New("container width does not match the valid sample bits")
		}
		sampleFormat = FormatPCMInteger
	case ieeeSubformatTag:
		if validBits != floatBitWidth || bitsPerSample != floatBitWidth {
			return nil, WAVInfo{}, fmt.Errorf("unsupported floating point width %d", validBits)
		}
		sampleFormat = FormatIEEEFloat
	default:
		return nil, WAVInfo{}, fmt.Errorf("unsupported WAV format code %d", effectiveTag)
	}

	samples, err := decodeSamples(effectiveTag, bitsPerSample, sampleChunk)
	if err != nil {
		return nil, WAVInfo{}, err
	}
	frameCount := int64(len(samples) / channels)
	return &Clip{
			SampleRate: sampleRate,
			Channels:   channels,
			Samples:    samples,
		}, WAVInfo{
			SampleRate:    sampleRate,
			Channels:      channels,
			BitsPerSample: bitsPerSample,
			SampleFormat:  sampleFormat,
			FrameCount:    frameCount,
			ByteCount:     int64(len(data)),
		}, nil
}

func resolveFormat(audioFormat uint16, bitsPerSample int, formatChunk []byte) (uint16, int, error) {
	switch audioFormat {
	case pcmFormatCode:
		return pcmSubformatTag, bitsPerSample, nil
	case ieeeFormatCode:
		return ieeeSubformatTag, bitsPerSample, nil
	case extFormatCode:
		if len(formatChunk) < 40 {
			return 0, 0, errors.New("extensible fmt chunk is too short")
		}
		validBits := int(binary.LittleEndian.Uint16(formatChunk[18:20]))
		subformat := formatChunk[24:40]
		var tail [14]byte
		copy(tail[:], subformat[2:16])
		if tail != subformatTail {
			return 0, 0, errors.New("unsupported extensible subformat GUID")
		}
		tag := binary.LittleEndian.Uint16(subformat[0:2])
		if tag != pcmSubformatTag && tag != ieeeSubformatTag {
			return 0, 0, fmt.Errorf("unsupported extensible format tag %d", tag)
		}
		return tag, validBits, nil
	default:
		return 0, 0, fmt.Errorf("unsupported WAV format code %d", audioFormat)
	}
}

func decodeSamples(tag uint16, bitsPerSample int, chunk []byte) ([]float32, error) {
	switch tag {
	case pcmSubformatTag:
		switch bitsPerSample {
		case pcmBitWidth16:
			samples := make([]float32, len(chunk)/2)
			for index := range samples {
				raw := int16(binary.LittleEndian.Uint16(chunk[index*2 : index*2+2]))
				if raw < 0 {
					samples[index] = float32(raw) / 32768
				} else {
					samples[index] = float32(raw) / 32767
				}
			}
			return samples, nil
		case pcmBitWidth24:
			samples := make([]float32, len(chunk)/3)
			for index := range samples {
				raw := int32(chunk[index*3]) |
					int32(chunk[index*3+1])<<8 |
					int32(chunk[index*3+2])<<16
				if raw&0x800000 != 0 {
					raw |= ^0xFFFFFF
				}
				if raw < 0 {
					samples[index] = float32(raw) / 8388608
				} else {
					samples[index] = float32(raw) / 8388607
				}
			}
			return samples, nil
		default:
			return nil, fmt.Errorf("unsupported integer PCM width %d", bitsPerSample)
		}
	case ieeeSubformatTag:
		samples := make([]float32, len(chunk)/4)
		for index := range samples {
			bits := binary.LittleEndian.Uint32(chunk[index*4 : index*4+4])
			value := math.Float32frombits(bits)
			if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
				return nil, errors.New("floating point samples must be finite")
			}
			samples[index] = value
		}
		return samples, nil
	default:
		return nil, errors.New("unsupported sample coding")
	}
}
