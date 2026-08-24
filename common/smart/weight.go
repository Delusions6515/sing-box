package smart

import (
	"math"
	"time"
)

const (
	DefaultMinSamples = 2
	AllowedWeight     = 0.4
)

type Scene uint8

const (
	SceneWeb Scene = iota
	SceneInteractive
	SceneStreaming
	SceneTransfer
)

type sceneParams struct {
	successRateWeight float64
	connectTimeWeight float64
	latencyWeight     float64
	trafficWeight     float64
	durationWeight    float64
	qualityWeight     float64
	minDecayFactor    float64
}

var presetSceneParams = [...]sceneParams{
	SceneWeb:         {0.5, 0.1, 0.4, 0.8, 0.6, 1.0, 0.2},
	SceneInteractive: {0.6, 0.1, 0.3, 1.2, 1.0, 1.3, 0.3},
	SceneStreaming:   {0.5, 0.2, 0.3, 1.5, 0.8, 1.2, 0.2},
	SceneTransfer:    {0.5, 0.2, 0.3, 1.8, 0.7, 0.9, 0.1},
}

// ModelInput contains the inputs tracked by the scorer. Traffic is in MB,
// rates are in KB/s, and connection duration is represented by a Duration.
type ModelInput struct {
	Success     int64
	Failure     int64
	ConnectTime time.Duration
	Latency     time.Duration

	UploadMB                  float64
	DownloadMB                float64
	MaxUploadRateKB           float64
	MaxDownloadRateKB         float64
	ConnectionDuration        time.Duration
	HistoryUploadMB           float64
	HistoryDownloadMB         float64
	HistoryMaxUploadRateKB    float64
	HistoryMaxDownloadRateKB  float64
	HistoryConnectionDuration time.Duration
	LossRate                  float64
	CumulativeLossRate        float64
	ASN                       string
	Target                    string
	DestinationIP             string
	LastUsed                  time.Time
	IsUDP                     bool
	ConnectionFailed          bool
}

// TimeDecay matches mihomo's current hourly-bucketed piecewise decay curve.
func TimeDecay(lastUsedTime, now int64, minDecay float64) float64 {
	fuzzyLastUsedTime := lastUsedTime / int64(time.Hour/time.Second) * int64(time.Hour/time.Second)
	hoursSinceLastConn := float64(now-fuzzyLastUsedTime) / time.Hour.Seconds()
	var decay float64
	switch {
	case hoursSinceLastConn <= 24:
		decay = 1
	case hoursSinceLastConn <= 72:
		decay = 1 - (hoursSinceLastConn-24)/48*0.2
	case hoursSinceLastConn <= 168:
		decay = 0.8 - (hoursSinceLastConn-72)/96*0.3
	case hoursSinceLastConn <= 720:
		decay = 0.5 - (hoursSinceLastConn-168)/552*0.2
	default:
		decay = 0.1
	}
	return math.Max(minDecay, decay)
}

// IdentifyConnectionScene ports mihomo's scene classifier.
func IdentifyConnectionScene(input ModelInput) Scene {
	latency := input.Latency.Milliseconds()
	durationMinutes := input.ConnectionDuration.Minutes()
	totalRate := (input.UploadMB + input.DownloadMB) / durationMinutes

	if (input.IsUDP && latency < 150 && durationMinutes > 3 &&
		input.UploadMB > 0.2 && input.DownloadMB > 0.2 &&
		input.MaxUploadRateKB > 200 && input.MaxDownloadRateKB > 200 &&
		totalRate > 0.1 && totalRate < 10) ||
		(!input.IsUDP && latency < 250 && durationMinutes > 3 &&
			input.UploadMB > 0.1 && input.DownloadMB > 0.1 &&
			input.UploadMB < 150 && input.DownloadMB < 150 &&
			input.UploadMB/input.DownloadMB > 0.2 && input.UploadMB/input.DownloadMB < 5 &&
			input.MaxUploadRateKB > 150 && input.MaxDownloadRateKB > 150 &&
			totalRate > 0.05 && totalRate < 15) {
		return SceneInteractive
	}
	if (input.UploadMB > 100 || input.DownloadMB > 100 || input.MaxUploadRateKB > 5000) && durationMinutes > 0.5 && totalRate > 5 {
		return SceneTransfer
	}
	if durationMinutes > 1 {
		downloadThroughput := input.DownloadMB / durationMinutes
		if (input.DownloadMB > 60 && input.DownloadMB/input.UploadMB > 3 && input.MaxDownloadRateKB > 2000 && input.MaxDownloadRateKB/input.MaxUploadRateKB > 4 && downloadThroughput > 5) ||
			(input.DownloadMB > 15 && input.DownloadMB/input.UploadMB > 3 && input.MaxDownloadRateKB > 1000 && input.MaxDownloadRateKB/input.MaxUploadRateKB > 3 && downloadThroughput > 2) {
			return SceneStreaming
		}
	}
	return SceneWeb
}

// CalculateWeight faithfully ports mihomo's non-LightGBM calculation. Loss and
// historical traffic-comparison inputs are not tracked, so those terms are zero.
func CalculateWeight(input ModelInput, now time.Time) (float64, bool) {
	return calculateWeight(input, now, DefaultMinSamples)
}

func calculateWeight(input ModelInput, now time.Time, minSamples int) (float64, bool) {
	total := input.Success + input.Failure
	if total < int64(minSamples) {
		return 0, true
	}
	scene := IdentifyConnectionScene(input)
	params := presetSceneParams[scene]
	timeFactor := 1.0
	if !input.LastUsed.IsZero() {
		timeFactor = TimeDecay(input.LastUsed.Unix(), now.Unix(), params.minDecayFactor)
	}
	decayedSuccess := float64(input.Success) * timeFactor
	decayedFailure := float64(input.Failure) * timeFactor
	decayedTotal := decayedSuccess + decayedFailure
	if decayedTotal < 1 {
		decayedSuccess = math.Max(0.5, decayedSuccess)
		decayedFailure = math.Max(0.5, decayedFailure)
		decayedTotal = decayedSuccess + decayedFailure
	}

	connectTime := input.ConnectTime.Milliseconds()
	latency := input.Latency.Milliseconds()
	if connectTime == 0 {
		if input.ConnectionFailed {
			connectTime = 2000
		} else {
			connectTime = 1
		}
	}
	if latency == 0 {
		if input.ConnectionFailed {
			latency = 2000
		} else {
			latency = 1
		}
	}
	successRate := decayedSuccess / decayedTotal
	connectScore := math.Min(0.8, math.Max(0.3, math.Exp(-float64(connectTime)/1500)*timeFactor))
	latencyScore := math.Min(0.8, math.Max(0.3, math.Exp(-float64(latency)/1500)*timeFactor))
	if input.IsUDP {
		params.latencyWeight = math.Min(0.5, params.latencyWeight*1.2)
		params.successRateWeight = math.Min(0.6, params.successRateWeight*1.1)
		params.connectTimeWeight = 1 - params.successRateWeight - params.latencyWeight
	}

	durationMinutes := input.ConnectionDuration.Minutes()
	isShortConnection := durationMinutes <= 1
	isLongConnection := durationMinutes > 10
	baseWeight := successRate*params.successRateWeight + connectScore*params.connectTimeWeight + latencyScore*params.latencyWeight
	trafficFactor := 0.0
	if input.UploadMB > 0 || input.DownloadMB > 0 {
		uploadFactor := calculateTrafficFactor(input.UploadMB, input.MaxUploadRateKB, durationMinutes, isShortConnection)
		downloadFactor := calculateTrafficFactor(input.DownloadMB, input.MaxDownloadRateKB, durationMinutes, isShortConnection)
		uploadWeight, downloadWeight := 0.4, 0.6
		if scene == SceneStreaming {
			uploadWeight, downloadWeight = 0.2, 0.8
		} else if scene == SceneTransfer && input.UploadMB > input.DownloadMB*2 {
			uploadWeight, downloadWeight = 0.7, 0.3
		}
		trafficFactor = uploadFactor*uploadWeight + downloadFactor*downloadWeight
	}
	durationFactor := 0.1
	if durationMinutes > 0 {
		switch {
		case isShortConnection:
			durationFactor = math.Min(0.3, 0.1+math.Log1p(durationMinutes)*0.08)
		case isLongConnection:
			durationFactor = math.Min(0.5, 0.2+math.Log1p(durationMinutes)*0.1)
		default:
			durationFactor = math.Min(0.4, 0.15+math.Log1p(durationMinutes)*0.09)
		}
	}
	qualityBonus := 0.0
	if latency > 0 && latency < 100 {
		qualityBonus += 0.1
	}
	if connectTime > 0 && connectTime < 10 {
		qualityBonus += 0.1
	}
	if (scene == SceneStreaming || scene == SceneTransfer) && input.DownloadMB > 20 {
		qualityBonus += 0.1
	}
	if scene == SceneInteractive && latency > 0 && latency < 100 && successRate > 0.9 {
		qualityBonus += 0.1
	}
	qualityBonus = math.Min(0.3, qualityBonus)
	return baseWeight * (1 + trafficFactor*params.trafficWeight + durationFactor*params.durationWeight + qualityBonus*params.qualityWeight), false
}

func calculateTrafficFactor(trafficMB, maxRateKB, durationMinutes float64, isShort bool) float64 {
	if trafficMB <= 0 || durationMinutes <= 0 {
		return 0
	}
	var baseFactor float64
	switch {
	case trafficMB < 0.005:
		baseFactor = 0.10 + 0.05*math.Log10(trafficMB/0.001)
	case trafficMB < 0.01:
		baseFactor = 0.18 + 0.08*math.Log10(trafficMB/0.005)
	case trafficMB < 0.05:
		baseFactor = 0.35 + 0.10*math.Log10(trafficMB/0.01)
	case trafficMB < 0.1:
		baseFactor = 0.53 + 0.15*math.Log10(trafficMB/0.05)
	case trafficMB < 0.5:
		baseFactor = 0.72 + 0.18*math.Log10(trafficMB/0.1)
	case trafficMB < 1:
		baseFactor = 0.98 + 0.15*math.Log10(trafficMB/0.5)
	case trafficMB < 5:
		baseFactor = 1.18 + 0.10*math.Log10(trafficMB)
	case trafficMB < 20:
		baseFactor = 1.32 + 0.08*math.Log10(trafficMB/5)
	case trafficMB < 100:
		baseFactor = 1.45 + 0.06*math.Log10(trafficMB/20)
	case trafficMB < 500:
		baseFactor = 1.56 + 0.05*math.Log10(trafficMB/100)
	case trafficMB < 3000:
		baseFactor = 1.66 + 0.04*math.Log10(trafficMB/500)
	default:
		baseFactor = 1.74 + 0.02*math.Log10(trafficMB/3000)
	}
	var rateBonus float64
	switch {
	case maxRateKB < 20:
		rateBonus = 1 + 0.05*(maxRateKB/20)
	case maxRateKB < 100:
		rateBonus = 1.05 + 0.05*((maxRateKB-20)/80)
	case maxRateKB < 500:
		rateBonus = 1.10 + 0.05*((maxRateKB-100)/400)
	case maxRateKB < 2000:
		rateBonus = 1.15 + 0.05*((maxRateKB-500)/1500)
	case maxRateKB < 5000:
		rateBonus = 1.20 + 0.04*((maxRateKB-2000)/3000)
	case maxRateKB < 20000:
		rateBonus = 1.24 + 0.04*((maxRateKB-5000)/15000)
	case maxRateKB < 100000:
		rateBonus = math.Min(1.32, 1.28+0.03*math.Log10(maxRateKB/20000))
	default:
		rateBonus = math.Min(1.36, 1.32+0.02*math.Log10(maxRateKB/100000))
	}
	throughputKBs := trafficMB * 1024 / math.Max(1, durationMinutes*60)
	accelBonus := 1.0
	if ratio := maxRateKB / throughputKBs; ratio > 2 {
		accelBonus += math.Min(0.12, 0.02*(ratio-2))
	}
	connectionFactor := 1.0
	throughput := trafficMB / math.Max(1, durationMinutes)
	if isShort {
		connectionFactor += 0.06 * math.Min(1, throughput/25)
	} else if throughput > 5 {
		connectionFactor += 0.05 * math.Min(1, (throughput-5)/80)
	}
	return math.Min(1.25, baseFactor*math.Min(1.25, rateBonus*accelBonus)*connectionFactor)
}
