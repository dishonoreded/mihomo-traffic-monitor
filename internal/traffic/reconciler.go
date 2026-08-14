package traffic

import (
	"net"
	"sort"
	"strings"
	"time"

	"golang.org/x/net/publicsuffix"
)

const UnknownProcess = "Unknown process"

type Class string

const (
	Observed     Class = "observed"
	Residual     Class = "residual"
	GapRecovered Class = "gap_recovered"
)

type Identity struct {
	App               string
	Host              string
	RegistrableDomain string
}

type Connection struct {
	ID            string
	Upload        int64
	Download      int64
	Process       string
	SniffHost     string
	Host          string
	DestinationIP string
}

type Candidate struct {
	Identity Identity
	Upload   int64
	Download int64
}

type Sample struct {
	At            time.Time
	UploadTotal   int64
	DownloadTotal int64
	Connections   []Connection
}

type Record struct {
	Minute            time.Time
	Class             Class
	App               string
	Host              string
	RegistrableDomain string
	Upload            int64
	Download          int64
}

type ledgerGlobal struct {
	sequence int64
	minute   time.Time
	upload   int64
	download int64
}

type ledgerCandidate struct {
	sequence int64
	identity Identity
	upload   int64
	download int64
}

type Reconciler struct {
	previous   *Sample
	sequence   int64
	globals    []ledgerGlobal
	candidates []ledgerCandidate
}

func NewReconciler() *Reconciler {
	return &Reconciler{}
}

func CanonicalIdentity(connection Connection) Identity {
	app := strings.TrimSpace(connection.Process)
	if app == "" {
		app = UnknownProcess
	}
	host := firstNonEmpty(connection.SniffHost, connection.Host, connection.DestinationIP)
	host = strings.TrimRight(strings.ToLower(strings.TrimSpace(host)), ".")
	domain := ""
	if net.ParseIP(host) == nil && host != "" {
		if registrable, err := publicsuffix.EffectiveTLDPlusOne(host); err == nil {
			domain = registrable
		}
	}
	return Identity{App: app, Host: host, RegistrableDomain: domain}
}

func CandidateDeltas(previous, current []Connection) []Candidate {
	byID := make(map[string]Connection, len(previous))
	for _, connection := range previous {
		byID[connection.ID] = connection
	}
	aggregated := make(map[Identity]Candidate)
	for _, connection := range current {
		identity := CanonicalIdentity(connection)
		upload := nonNegative(connection.Upload)
		download := nonNegative(connection.Download)
		if old, exists := byID[connection.ID]; exists && CanonicalIdentity(old) == identity {
			upload = counterGrowth(connection.Upload, old.Upload)
			download = counterGrowth(connection.Download, old.Download)
		}
		if upload == 0 && download == 0 {
			continue
		}
		delta := aggregated[identity]
		delta.Identity = identity
		delta.Upload += upload
		delta.Download += download
		aggregated[identity] = delta
	}
	result := make([]Candidate, 0, len(aggregated))
	for _, delta := range aggregated {
		result = append(result, delta)
	}
	sort.Slice(result, func(left, right int) bool {
		if result[left].Identity.App != result[right].Identity.App {
			return result[left].Identity.App < result[right].Identity.App
		}
		return result[left].Identity.Host < result[right].Identity.Host
	})
	return result
}

func (reconciler *Reconciler) Add(current Sample) []Record {
	current = cloneSample(current)
	if reconciler.previous == nil {
		reconciler.previous = &current
		return nil
	}
	previous := reconciler.previous
	if current.UploadTotal < previous.UploadTotal || current.DownloadTotal < previous.DownloadTotal {
		reconciler.globals = nil
		reconciler.candidates = nil
		reconciler.previous = &current
		return nil
	}

	reconciler.sequence++
	records := reconciler.expire(reconciler.sequence - 2)
	uploadGrowth := counterGrowth(current.UploadTotal, previous.UploadTotal)
	downloadGrowth := counterGrowth(current.DownloadTotal, previous.DownloadTotal)
	if uploadGrowth > 0 || downloadGrowth > 0 {
		reconciler.globals = append(reconciler.globals, ledgerGlobal{
			sequence: reconciler.sequence,
			minute:   current.At.UTC().Truncate(time.Minute),
			upload:   uploadGrowth,
			download: downloadGrowth,
		})
	}
	for _, delta := range CandidateDeltas(previous.Connections, current.Connections) {
		reconciler.candidates = append(reconciler.candidates, ledgerCandidate{
			sequence: reconciler.sequence,
			identity: delta.Identity,
			upload:   delta.Upload,
			download: delta.Download,
		})
	}
	records = append(records, reconciler.match()...)
	reconciler.compact()
	reconciler.previous = &current
	return aggregateRecords(records)
}

func (reconciler *Reconciler) Flush() []Record {
	records := make([]Record, 0, len(reconciler.globals))
	for _, global := range reconciler.globals {
		if global.upload > 0 || global.download > 0 {
			records = append(records, Record{Minute: global.minute, Class: Residual, Upload: global.upload, Download: global.download})
		}
	}
	reconciler.globals = nil
	reconciler.candidates = nil
	return aggregateRecords(records)
}

func (reconciler *Reconciler) expire(maxSequence int64) []Record {
	var records []Record
	for index := range reconciler.globals {
		global := &reconciler.globals[index]
		if global.sequence <= maxSequence && (global.upload > 0 || global.download > 0) {
			records = append(records, Record{Minute: global.minute, Class: Residual, Upload: global.upload, Download: global.download})
			global.upload = 0
			global.download = 0
		}
	}
	for index := range reconciler.candidates {
		candidate := &reconciler.candidates[index]
		if candidate.sequence <= maxSequence {
			candidate.upload = 0
			candidate.download = 0
		}
	}
	reconciler.compact()
	return records
}

func (reconciler *Reconciler) match() []Record {
	var records []Record
	for globalIndex := range reconciler.globals {
		global := &reconciler.globals[globalIndex]
		for candidateIndex := range reconciler.candidates {
			candidate := &reconciler.candidates[candidateIndex]
			upload := min(global.upload, candidate.upload)
			download := min(global.download, candidate.download)
			if upload == 0 && download == 0 {
				continue
			}
			records = append(records, Record{
				Minute: global.minute, Class: Observed,
				App: candidate.identity.App, Host: candidate.identity.Host, RegistrableDomain: candidate.identity.RegistrableDomain,
				Upload: upload, Download: download,
			})
			global.upload -= upload
			global.download -= download
			candidate.upload -= upload
			candidate.download -= download
			if global.upload == 0 && global.download == 0 {
				break
			}
		}
	}
	return records
}

func (reconciler *Reconciler) compact() {
	globals := reconciler.globals[:0]
	for _, global := range reconciler.globals {
		if global.upload > 0 || global.download > 0 {
			globals = append(globals, global)
		}
	}
	reconciler.globals = globals
	candidates := reconciler.candidates[:0]
	for _, candidate := range reconciler.candidates {
		if candidate.upload > 0 || candidate.download > 0 {
			candidates = append(candidates, candidate)
		}
	}
	reconciler.candidates = candidates
}

func aggregateRecords(records []Record) []Record {
	type key struct {
		minute            time.Time
		class             Class
		app               string
		host              string
		registrableDomain string
	}
	aggregated := make(map[key]Record)
	for _, record := range records {
		if record.Upload == 0 && record.Download == 0 {
			continue
		}
		identity := key{record.Minute, record.Class, record.App, record.Host, record.RegistrableDomain}
		value := aggregated[identity]
		value.Minute = record.Minute
		value.Class = record.Class
		value.App = record.App
		value.Host = record.Host
		value.RegistrableDomain = record.RegistrableDomain
		value.Upload += record.Upload
		value.Download += record.Download
		aggregated[identity] = value
	}
	result := make([]Record, 0, len(aggregated))
	for _, record := range aggregated {
		result = append(result, record)
	}
	sort.Slice(result, func(left, right int) bool {
		if !result[left].Minute.Equal(result[right].Minute) {
			return result[left].Minute.Before(result[right].Minute)
		}
		if result[left].Class != result[right].Class {
			return result[left].Class < result[right].Class
		}
		if result[left].App != result[right].App {
			return result[left].App < result[right].App
		}
		return result[left].Host < result[right].Host
	})
	return result
}

func cloneSample(sample Sample) Sample {
	sample.At = sample.At.UTC()
	sample.Connections = append([]Connection(nil), sample.Connections...)
	return sample
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func counterGrowth(current, previous int64) int64 {
	if current < previous {
		return nonNegative(current)
	}
	return current - previous
}

func nonNegative(value int64) int64 {
	return max(value, 0)
}
