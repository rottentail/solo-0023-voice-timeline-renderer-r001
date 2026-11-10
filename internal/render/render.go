package render

import (
	"context"
	"errors"
	"time"

	"voice-timeline-renderer/internal/audiocodec"
	"voice-timeline-renderer/internal/domain"
)

type Limits struct {
	MaxOutputFrames int64
}

type Result struct {
	WAV     []byte
	Receipt domain.RenderReceipt
	Output  domain.OutputAsset
}

func Render(
	ctx context.Context,
	renderID string,
	plan domain.Timeline,
	sources map[string]SourceInput,
	limits Limits,
	now time.Time,
) (Result, error) {
	spans, totalFrames, err := prepareSpans(ctx, plan, sources)
	if err != nil {
		return Result{}, err
	}
	if totalFrames < 1 {
		return Result{}, errors.New("timeline produced no output frames")
	}
	if limits.MaxOutputFrames > 0 && totalFrames > limits.MaxOutputFrames {
		return Result{}, errors.New("rendered timeline exceeds the configured frame limit")
	}
	mix, _, err := mixPrepared(ctx, spans, totalFrames, plan.OutputChannels)
	if err != nil {
		return Result{}, err
	}
	normalized := normalizePeak(mix, plan.TargetPeakDBFS)
	outputWidth := domain.NormalizeOutputWidth(plan.OutputBitsPerSample)
	encoded, err := encodeOutput(plan.SampleRate, plan.OutputChannels, outputWidth, normalized.Samples)
	if err != nil {
		return Result{}, err
	}
	receipt, output, err := buildReceipt(renderID, plan, encoded, normalized, spans, now)
	if err != nil {
		return Result{}, err
	}
	return Result{WAV: encoded, Receipt: receipt, Output: output}, nil
}

func encodeOutput(sampleRate, channels, bitsPerSample int, samples []float32) ([]byte, error) {
	switch bitsPerSample {
	case domain.OutputWidth16:
		return audiocodec.EncodePCM16(sampleRate, channels, samples)
	case domain.OutputWidth24:
		return audiocodec.EncodePCM24(sampleRate, channels, samples)
	default:
		return nil, errors.New("render output must use 16-bit or 24-bit integer PCM")
	}
}
