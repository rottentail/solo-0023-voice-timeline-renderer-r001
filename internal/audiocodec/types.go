package audiocodec

// Sample format identifiers carried alongside the bit width so callers can
// distinguish integer PCM from IEEE float at the same or different widths.
const (
	FormatPCMInteger = "pcm_integer"
	FormatIEEEFloat  = "ieee_float"
)

// Accepted source and output widths.
const (
	pcmBitWidth16 = 16
	pcmBitWidth24 = 24
	floatBitWidth = 32
)

type Clip struct {
	SampleRate int
	Channels   int
	Samples    []float32
}

func (clip *Clip) FrameCount() int64 {
	if clip == nil || clip.Channels <= 0 {
		return 0
	}
	return int64(len(clip.Samples) / clip.Channels)
}

type WAVInfo struct {
	SampleRate    int
	Channels      int
	BitsPerSample int
	SampleFormat  string
	FrameCount    int64
	ByteCount     int64
}

type Metrics struct {
	PeakDBFS float64
	RMSDBFS  float64
}
