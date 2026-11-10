package render

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"voice-timeline-renderer/internal/audiocodec"
	"voice-timeline-renderer/internal/domain"
)

func buildReceipt(
	renderID string,
	plan domain.Timeline,
	encoded []byte,
	normalized normalizationResult,
	spans []preparedSpan,
	now time.Time,
) (domain.RenderReceipt, domain.OutputAsset, error) {
	decoded, info, err := audiocodec.DecodeWAV(encoded)
	if err != nil {
		return domain.RenderReceipt{}, domain.OutputAsset{}, err
	}
	requestedWidth := domain.NormalizeOutputWidth(plan.OutputBitsPerSample)
	if err := verifyOutputFacts(info, plan, requestedWidth); err != nil {
		return domain.RenderReceipt{}, domain.OutputAsset{}, err
	}
	measurements := audiocodec.Measure(decoded.Samples)
	digest := sha256.Sum256(encoded)
	digestText := hex.EncodeToString(digest[:])
	output := domain.OutputAsset{
		Digest:        digestText,
		ByteCount:     int64(len(encoded)),
		SampleRate:    info.SampleRate,
		Channels:      info.Channels,
		BitsPerSample: info.BitsPerSample,
		SampleFormat:  info.SampleFormat,
		FrameCount:    info.FrameCount,
		DurationMS:    framesToMilliseconds(info.FrameCount, info.SampleRate),
		PeakDBFS:      measurements.PeakDBFS,
		RMSDBFS:       measurements.RMSDBFS,
	}
	receiptSpans := make([]domain.ReceiptSpan, 0, len(spans))
	warnings := make([]string, 0)
	for _, span := range spans {
		receiptSpans = append(receiptSpans, domain.ReceiptSpan{
			Index:               span.Index,
			SourceID:            span.SourceID,
			SourceDigest:        span.SourceDigest,
			SourceBitsPerSample: span.SourceBitsPerSample,
			SourceSampleFormat:  span.SourceSampleFormat,
			SourceStartFrame:    span.SourceStartFrame,
			SourceEndFrame:      span.SourceEndFrame,
			OutputStartFrame:    span.OutputStartFrame,
			OutputEndFrame:      span.OutputEndFrame,
			OutputFrames:        span.OutputFrames,
			CrossfadeFrames:     span.CrossfadeFrames,
			GainDB:              span.GainDB,
			ChannelAction:       span.ChannelAction,
		})
		if span.ChannelAction != "unchanged" {
			warnings = append(warnings, fmt.Sprintf("span %d applied %s", span.Index, span.ChannelAction))
		}
		if sourceExceedsOutput(span, requestedWidth) {
			warnings = append(warnings, fmt.Sprintf(
				"span %d source precision %d-bit %s exceeds %d-bit integer output",
				span.Index,
				span.SourceBitsPerSample,
				span.SourceSampleFormat,
				requestedWidth,
			))
		}
	}
	receipt := domain.RenderReceipt{
		SchemaVersion: 1,
		RenderID:      renderID,
		TimelineID:    plan.ID,
		Output:        output,
		Normalization: domain.NormalizationReceipt{
			RequestedPeakDBFS: plan.TargetPeakDBFS,
			BeforePeakDBFS:    normalized.Before.PeakDBFS,
			AppliedGainDB:     normalized.GainDB,
			OutputPeakDBFS:    measurements.PeakDBFS,
		},
		Spans:     receiptSpans,
		Warnings:  warnings,
		CreatedAt: now.UTC(),
	}
	return receipt, output, nil
}

// verifyOutputFacts guarantees the encoded file matches every format fact the
// timeline and receipt will claim. A mismatch aborts the render before any
// artifact is published, so a success state can never carry a wrong-format
// downloadable file.
func verifyOutputFacts(info audiocodec.WAVInfo, plan domain.Timeline, requestedWidth int) error {
	if info.SampleFormat != audiocodec.FormatPCMInteger {
		return errors.New("encoded output is not integer PCM")
	}
	if info.BitsPerSample != requestedWidth {
		return fmt.Errorf("encoded output width %d does not match requested width %d", info.BitsPerSample, requestedWidth)
	}
	if info.SampleRate != plan.SampleRate {
		return errors.New("encoded output sample rate does not match the timeline")
	}
	if info.Channels != plan.OutputChannels {
		return errors.New("encoded output channel count does not match the timeline")
	}
	if info.FrameCount < 1 {
		return errors.New("encoded output contains no frames")
	}
	return nil
}

func sourceExceedsOutput(span preparedSpan, outputWidth int) bool {
	if span.SourceSampleFormat == domain.SampleFormatIEEEFloat {
		return true
	}
	return span.SourceSampleFormat == domain.SampleFormatPCMInteger && span.SourceBitsPerSample > outputWidth
}

func framesToMilliseconds(frames int64, sampleRate int) int64 {
	if frames <= 0 || sampleRate <= 0 {
		return 0
	}
	return int64(float64(frames) * 1000 / float64(sampleRate))
}
