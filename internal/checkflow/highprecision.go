package checkflow

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"

	"voice-timeline-renderer/internal/audiocodec"
	"voice-timeline-renderer/internal/domain"
)

// runHighPrecision exercises the full delivery chain with 24-bit integer PCM
// and 32-bit IEEE float sources, mixed-precision timelines, 16/24-bit renders,
// deterministic re-renders, and failure paths that must leave no artifact.
func runHighPrecision(ctx context.Context, harness *Harness) error {
	pcm16, err := sineWAV(48000, 1, 200, 220, 0.3)
	if err != nil {
		return err
	}
	pcm24, err := sineWAV24(48000, 2, 200, 330, 0.4)
	if err != nil {
		return err
	}
	flt32, err := sineWAVFloat32(44100, 1, 200, 440, 0.5)
	if err != nil {
		return err
	}

	source16, status, err := harness.createSource(ctx, "hp_source_pcm16", "16-bit take", pcm16)
	if err != nil || status != 201 {
		return fmt.Errorf("16-bit source registration failed: status=%d err=%v", status, err)
	}
	if source16.SampleFormat != domain.SampleFormatPCMInteger || source16.BitsPerSample != 16 {
		return fmt.Errorf("16-bit source facts wrong: format=%s bits=%d", source16.SampleFormat, source16.BitsPerSample)
	}
	source24, status, err := harness.createSource(ctx, "hp_source_pcm24", "24-bit take", pcm24)
	if err != nil || status != 201 {
		return fmt.Errorf("24-bit source registration failed: status=%d err=%v", status, err)
	}
	if source24.SampleFormat != domain.SampleFormatPCMInteger || source24.BitsPerSample != 24 {
		return fmt.Errorf("24-bit source facts wrong: format=%s bits=%d", source24.SampleFormat, source24.BitsPerSample)
	}
	sourceFloat, status, err := harness.createSource(ctx, "hp_source_float32", "float take", flt32)
	if err != nil || status != 201 {
		return fmt.Errorf("32-bit float source registration failed: status=%d err=%v", status, err)
	}
	if sourceFloat.SampleFormat != domain.SampleFormatIEEEFloat || sourceFloat.BitsPerSample != 32 {
		return fmt.Errorf("float source facts wrong: format=%s bits=%d", sourceFloat.SampleFormat, sourceFloat.BitsPerSample)
	}

	// Read-back must expose the same precision facts.
	for _, want := range []domain.SourceAsset{source16, source24, sourceFloat} {
		readBack, readStatus, readErr := harness.getSource(ctx, want.ID)
		if readErr != nil || readStatus != 200 {
			return fmt.Errorf("source %s read-back failed: status=%d err=%v", want.ID, readStatus, readErr)
		}
		if readBack.SampleFormat != want.SampleFormat || readBack.BitsPerSample != want.BitsPerSample ||
			readBack.FrameCount != want.FrameCount || readBack.Digest != want.Digest {
			return fmt.Errorf("source %s read-back facts diverged: %+v", want.ID, readBack)
		}
	}

	spans := []domain.TimelineSpan{
		{SourceID: "hp_source_pcm16", SourceStartFrame: 0, SourceEndFrame: 9600, GainDB: -2, CrossfadeFrames: 0},
		{SourceID: "hp_source_pcm24", SourceStartFrame: 0, SourceEndFrame: 9600, GainDB: 0, CrossfadeFrames: 2400},
		{SourceID: "hp_source_float32", SourceStartFrame: 0, SourceEndFrame: 8820, GainDB: -1, CrossfadeFrames: 1200},
	}

	if err := renderAndVerify(ctx, harness, "hp_timeline_24", "hp_render_24", 24, spans, source16, source24, sourceFloat); err != nil {
		return err
	}
	if err := renderAndVerify(ctx, harness, "hp_timeline_16", "hp_render_16", 16, spans, source16, source24, sourceFloat); err != nil {
		return err
	}

	// Re-rendering the same project under a new job id must reproduce the same
	// audio bytes and format conclusions.
	if err := assertDeterministic(ctx, harness, "hp_timeline_24", "hp_render_24", "hp_render_24_again", 24); err != nil {
		return err
	}

	// An unsupported output width is rejected at timeline creation and never
	// produces a render or downloadable artifact.
	badStatus, body, err := harness.requestJSON(ctx, "PUT", "/v1/timelines/hp_timeline_bad", struct {
		Name                string                `json:"name"`
		SampleRate          int                   `json:"sample_rate"`
		OutputChannels      int                   `json:"output_channels"`
		OutputBitsPerSample int                   `json:"output_bits_per_sample"`
		TargetPeakDBFS      float64               `json:"target_peak_dbfs"`
		Spans               []domain.TimelineSpan `json:"spans"`
	}{
		Name:                "unsupported width",
		SampleRate:          48000,
		OutputChannels:      2,
		OutputBitsPerSample: 32,
		TargetPeakDBFS:      -1,
		Spans:               spans[:1],
	})
	if err != nil {
		return err
	}
	if badStatus != 422 || failureCode(body) != "invalid_output_width" {
		return fmt.Errorf("32-bit output width returned status=%d code=%s, want 422 invalid_output_width", badStatus, failureCode(body))
	}
	missingJob, missingStatus, err := harness.getRender(ctx, "hp_render_never")
	if err != nil {
		return err
	}
	if missingStatus != 404 || missingJob.ID != "" {
		return fmt.Errorf("unexpected render artifact state: status=%d id=%s", missingStatus, missingJob.ID)
	}
	return nil
}

func renderAndVerify(
	ctx context.Context,
	harness *Harness,
	timelineID, renderID string,
	outputBits int,
	spans []domain.TimelineSpan,
	sources ...domain.SourceAsset,
) error {
	plan, status, err := harness.createTimeline(ctx, timelineID, "mixed precision assembly", 48000, 2, outputBits, -1, spans)
	if err != nil || status != 201 {
		return fmt.Errorf("timeline %s creation failed: status=%d err=%v", timelineID, status, err)
	}
	if plan.OutputBitsPerSample != outputBits {
		return fmt.Errorf("timeline %s stored width %d, want %d", timelineID, plan.OutputBitsPerSample, outputBits)
	}
	job, status, err := harness.createRender(ctx, renderID, plan.ID)
	if err != nil || status != 201 || job.State != domain.RenderCompleted || job.Output == nil {
		return fmt.Errorf("render %s failed: status=%d state=%s err=%v", renderID, status, job.State, err)
	}
	if job.Output.SampleFormat != domain.SampleFormatPCMInteger || job.Output.BitsPerSample != outputBits {
		return fmt.Errorf("render %s job facts wrong: format=%s bits=%d", renderID, job.Output.SampleFormat, job.Output.BitsPerSample)
	}

	receipt, status, err := harness.getReceipt(ctx, renderID)
	if err != nil || status != 200 {
		return fmt.Errorf("receipt %s read failed: status=%d err=%v", renderID, status, err)
	}
	if receipt.Output.SampleFormat != domain.SampleFormatPCMInteger ||
		receipt.Output.BitsPerSample != outputBits ||
		receipt.Output.Channels != 2 ||
		receipt.Output.SampleRate != 48000 {
		return fmt.Errorf("receipt %s output facts wrong: %+v", renderID, receipt.Output)
	}
	if len(receipt.Spans) != len(spans) {
		return fmt.Errorf("receipt %s span count %d, want %d", renderID, len(receipt.Spans), len(spans))
	}
	sourceByID := make(map[string]domain.SourceAsset, len(sources))
	for _, asset := range sources {
		sourceByID[asset.ID] = asset
	}
	for index, span := range receipt.Spans {
		want := sourceByID[span.SourceID]
		if span.SourceSampleFormat != want.SampleFormat || span.SourceBitsPerSample != want.BitsPerSample {
			return fmt.Errorf("receipt span %d lost source precision: got %d-bit %s, want %d-bit %s",
				index, span.SourceBitsPerSample, span.SourceSampleFormat, want.BitsPerSample, want.SampleFormat)
		}
	}

	audioBytes, status, err := harness.getOutput(ctx, renderID)
	if err != nil || status != 200 {
		return fmt.Errorf("output %s read failed: status=%d err=%v", renderID, status, err)
	}
	digest := sha256.Sum256(audioBytes)
	if hex.EncodeToString(digest[:]) != receipt.Output.Digest {
		return fmt.Errorf("render %s output digest disagrees with receipt", renderID)
	}
	clip, info, err := audiocodec.DecodeWAV(audioBytes)
	if err != nil {
		return fmt.Errorf("render %s output not decodable: %w", renderID, err)
	}
	if info.SampleFormat != domain.SampleFormatPCMInteger || info.BitsPerSample != outputBits {
		return fmt.Errorf("render %s downloaded WAV facts wrong: format=%s bits=%d", renderID, info.SampleFormat, info.BitsPerSample)
	}
	if info.FrameCount != receipt.Output.FrameCount || info.Channels != 2 || info.SampleRate != 48000 {
		return fmt.Errorf("render %s WAV header disagrees with receipt", renderID)
	}
	measured := audiocodec.Measure(clip.Samples)
	if math.Abs(measured.PeakDBFS-(-1)) > 0.25 {
		return fmt.Errorf("render %s peak %.3f dBFS misses target", renderID, measured.PeakDBFS)
	}
	if receipt.Output.PeakDBFS != measured.PeakDBFS || receipt.Output.RMSDBFS != measured.RMSDBFS {
		return fmt.Errorf("render %s receipt measurements disagree with re-decoded WAV", renderID)
	}
	return nil
}

func assertDeterministic(ctx context.Context, harness *Harness, timelineID, firstRender, secondRender string, outputBits int) error {
	first, status, err := harness.getOutput(ctx, firstRender)
	if err != nil || status != 200 {
		return fmt.Errorf("first output read failed: status=%d err=%v", status, err)
	}
	firstReceipt, status, err := harness.getReceipt(ctx, firstRender)
	if err != nil || status != 200 {
		return fmt.Errorf("first receipt read failed: status=%d err=%v", status, err)
	}
	job, status, err := harness.createRender(ctx, secondRender, timelineID)
	if err != nil || status != 201 || job.State != domain.RenderCompleted {
		return fmt.Errorf("second render failed: status=%d state=%s err=%v", status, job.State, err)
	}
	second, status, err := harness.getOutput(ctx, secondRender)
	if err != nil || status != 200 {
		return fmt.Errorf("second output read failed: status=%d err=%v", status, err)
	}
	secondReceipt, status, err := harness.getReceipt(ctx, secondRender)
	if err != nil || status != 200 {
		return fmt.Errorf("second receipt read failed: status=%d err=%v", status, err)
	}
	if sha256Digest(first) != sha256Digest(second) {
		return fmt.Errorf("re-rendered audio bytes differ from the first render")
	}
	if secondReceipt.Output.Digest != firstReceipt.Output.Digest ||
		secondReceipt.Output.BitsPerSample != outputBits ||
		secondReceipt.Output.SampleFormat != domain.SampleFormatPCMInteger ||
		secondReceipt.Output.FrameCount != firstReceipt.Output.FrameCount ||
		secondReceipt.Output.PeakDBFS != firstReceipt.Output.PeakDBFS ||
		secondReceipt.Output.RMSDBFS != firstReceipt.Output.RMSDBFS {
		return fmt.Errorf("re-rendered format conclusions differ: first=%+v second=%+v", firstReceipt.Output, secondReceipt.Output)
	}
	return nil
}

func sha256Digest(payload []byte) string {
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:])
}
